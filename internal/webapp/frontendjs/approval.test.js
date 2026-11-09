"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createHarness, findArticle, findButton, jsonResponse, openTab } = require("./harness.js");

// gatedSseResponse yields the events before waitBeforeIndex at once, waits for release() and only
// then yields the rest. That keeps /api/messages blocked exactly while the user decides, which is
// what the approval card must survive.
function gatedSseResponse(events, waitBeforeIndex) {
  const encoder = new TextEncoder();
  const chunks = events.map((event) => encoder.encode(`event: ${event.name}\ndata: ${JSON.stringify(event.data)}\n\n`));
  let release;
  const gate = new Promise((resolve) => { release = resolve; });
  return {
    response: {
      ok: true,
      status: 200,
      async json() { return null; },
      body: {
        getReader() {
          let index = 0;
          return {
            async read() {
              if (index === waitBeforeIndex) await gate;
              if (index < chunks.length) return { done: false, value: chunks[index++] };
              return { done: true };
            },
          };
        },
      },
    },
    release() { release(); },
  };
}

async function sendChatMessage(harness, text) {
  const content = harness.element("content");
  const input = content.querySelector(".chat-input");
  input.value = text;
  input.dispatchEvent({ type: "input" });
  findButton(content, "Отправить").click();
  await harness.flush();
  await harness.flush();
}

test("approval card approves over the API and the same stream continues", async () => {
  const harness = await createHarness();
  const gated = gatedSseResponse([
    { name: "approval", data: { token: "tok-1", tool: "web_search", summary: '{"query":"web approval"}', expires_in_seconds: 120 } },
    { name: "delta", data: { text: "Финал" } },
    { name: "done", data: { text: "Финальный ответ", message_id: 55 } },
  ], 1);
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/messages") return gated.response;
    if (request.path === "/api/chat/approval") {
      gated.release();
      return jsonResponse({ ok: true });
    }
    if (request.path.startsWith("/api/history?")) return jsonResponse({ items: [], next_before_id: null });
    return fallback();
  });
  await openTab(harness, "chat");
  await sendChatMessage(harness, "Подготовь обзор");

  let card = harness.element("content").querySelector(".chat-approval");
  assert.ok(card, "the inline approval card is missing");
  assert.match(card.textContent, /Подтверждение сетевого действия/);
  assert.match(card.textContent, /Веб-поиск/);
  assert.match(card.textContent, /web approval/);
  assert.match(card.textContent, /Истекает через \d+ с/);
  assert.equal(findButton(card, "Разрешить").disabled, false);
  assert.equal(findButton(card, "Отклонить").disabled, false);

  findButton(card, "Разрешить").click();
  await harness.flush();
  await harness.flush();
  await harness.flush();

  const decisions = harness.requests.filter((request) => request.path === "/api/chat/approval");
  assert.equal(decisions.length, 1);
  assert.deepEqual(JSON.parse(decisions[0].options.body), { token: "tok-1", approve: true });
  assert.equal(decisions[0].options.headers.get("Authorization"), "Bearer test-session");

  card = harness.element("content").querySelector(".chat-approval");
  assert.ok(card, "the approval card disappeared after the decision");
  assert.match(card.textContent, /Разрешено/);
  assert.equal(card.querySelectorAll("button").length, 0, "buttons must be disabled/gone after the decision");
  assert.ok(findArticle(harness.element("content"), "Финальный ответ"));
});

test("denying an approval posts approve=false and shows the localized refusal", async () => {
  const harness = await createHarness();
  const gated = gatedSseResponse([
    { name: "approval", data: { token: "tok-2", tool: "read_url", summary: '{"url":"https://example.com/"}', expires_in_seconds: 120 } },
    { name: "done", data: { text: "Сетевое действие отменено. Запрос не выполнен." } },
  ], 1);
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/messages") return gated.response;
    if (request.path === "/api/chat/approval") {
      gated.release();
      return jsonResponse({ ok: true });
    }
    if (request.path.startsWith("/api/history?")) return jsonResponse({ items: [], next_before_id: null });
    return fallback();
  });
  await openTab(harness, "chat");
  await sendChatMessage(harness, "Прочитай страницу");

  const card = harness.element("content").querySelector(".chat-approval");
  assert.ok(card);
  assert.match(card.textContent, /Чтение страницы/);
  findButton(card, "Отклонить").click();
  await harness.flush();
  await harness.flush();
  await harness.flush();

  const decisions = harness.requests.filter((request) => request.path === "/api/chat/approval");
  assert.equal(decisions.length, 1);
  assert.deepEqual(JSON.parse(decisions[0].options.body), { token: "tok-2", approve: false });
  const updated = harness.element("content").querySelector(".chat-approval");
  assert.ok(updated);
  assert.match(updated.textContent, /Отклонено/);
  assert.ok(findArticle(harness.element("content"), "Сетевое действие отменено"));
});

test("a timed-out approval shows the localized expiry message and never posts", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/messages") {
      return {
        ok: true,
        status: 200,
        async json() { return null; },
        body: {
          getReader() {
            const encoder = new TextEncoder();
            const chunks = [
              { name: "approval", data: { token: "tok-3", tool: "web_search", summary: '{"query":"q"}', expires_in_seconds: 1 } },
              { name: "error", data: { code: "chat_failed", message: "Время подтверждения сетевого действия истекло. Запрос не выполнен. Попробуй ещё раз." } },
            ].map((event) => encoder.encode(`event: ${event.name}\ndata: ${JSON.stringify(event.data)}\n\n`));
            let index = 0;
            return {
              async read() {
                if (index < chunks.length) return { done: false, value: chunks[index++] };
                return { done: true };
              },
            };
          },
        },
      };
    }
    if (request.path.startsWith("/api/history?")) return jsonResponse({ items: [], next_before_id: null });
    return fallback();
  });
  await openTab(harness, "chat");
  await sendChatMessage(harness, "Подготовь обзор");
  await harness.flush();

  const card = harness.element("content").querySelector(".chat-approval");
  assert.ok(card, "the approval card is missing after the timeout");
  assert.match(card.textContent, /Время подтверждения истекло/);
  assert.match(harness.element("error").textContent, /истекло/);
  assert.equal(harness.requests.filter((request) => request.path === "/api/chat/approval").length, 0);
});
