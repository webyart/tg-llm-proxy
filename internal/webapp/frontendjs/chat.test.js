"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createHarness, findArticle, findButton, jsonResponse, openTab } = require("./harness.js");

function assistantHistory(feedback) {
  return {
    items: [{
      id: 7,
      role: "assistant",
      content: "**ответ** с `кодом`",
      created_at: "2026-01-01",
      feedback,
    }],
    next_before_id: null,
  };
}

function sseResponse(events) {
  const encoder = new TextEncoder();
  const chunks = events.map((event) => encoder.encode(`event: ${event.name}\ndata: ${JSON.stringify(event.data)}\n\n`));
  return {
    ok: true,
    status: 200,
    async json() { return null; },
    body: {
      getReader() {
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

test("chat renders a markdown answer with feedback buttons from history state", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/history?")) return jsonResponse(assistantHistory(1));
    return fallback();
  });
  await openTab(harness, "chat");

  const content = harness.element("content");
  assert.ok(content.querySelector(".chat-panel"));
  assert.ok(content.querySelector(".chat-input"));
  const article = findArticle(content, "ответ");
  assert.equal(article.querySelectorAll("strong").length, 1);
  assert.equal(article.querySelectorAll("code").length, 1);
  const up = findButton(article, "👍");
  const down = findButton(article, "👎");
  assert.equal(up.getAttribute("aria-pressed"), "true");
  assert.equal(down.getAttribute("aria-pressed"), "false");
});

test("chat feedback uses the stored message id without re-rendering the chat", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/history?")) return jsonResponse(assistantHistory(0));
    if (request.path === "/api/history/7/feedback") return jsonResponse({ ok: true, value: -1 });
    return fallback();
  });
  await openTab(harness, "chat");
  const content = harness.element("content");
  const article = findArticle(content, "ответ");
  const statusLoads = harness.requests.filter((request) => request.path === "/api/status").length;
  const down = findButton(article, "👎");
  down.click();
  await harness.flush();

  const feedback = harness.requests.filter((request) => request.path === "/api/history/7/feedback");
  assert.equal(feedback.length, 1);
  assert.equal(feedback[0].options.method, "POST");
  assert.equal(feedback[0].options.headers.get("Authorization"), "Bearer test-session");
  assert.deepEqual(JSON.parse(feedback[0].options.body), { value: -1 });
  assert.equal(harness.requests.filter((request) => request.path === "/api/status").length, statusLoads);
  assert.notEqual(article.parentNode, null);
  assert.equal(content.querySelectorAll(".chat-panel").length, 1);
  assert.equal(findButton(article, "👎").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(article, "👍").getAttribute("aria-pressed"), "false");
  assert.equal(harness.element("loading").hidden, true);
  assert.equal(harness.element("notice").textContent, "Оценка сохранена.");
});

test("a failed chat feedback click reverts the optimistic state", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/history?")) return jsonResponse(assistantHistory(1));
    if (request.path === "/api/history/7/feedback") {
      return jsonResponse({ error: { code: "history_mutation_failed" } }, 500);
    }
    return fallback();
  });
  await openTab(harness, "chat");
  const article = findArticle(harness.element("content"), "ответ");
  findButton(article, "👎").click();
  await harness.flush();

  assert.equal(findButton(article, "👍").getAttribute("aria-pressed"), "true");
  assert.equal(findButton(article, "👎").getAttribute("aria-pressed"), "false");
  assert.equal(harness.element("error").textContent, "Не удалось изменить историю.");
  assert.equal(harness.element("notice").hidden, true);
});

test("a streamed answer renders markdown and gets feedback from the done message_id", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/messages") {
      return sseResponse([
        { name: "stage", data: { stage: "thinking" } },
        { name: "delta", data: { text: "**При" } },
        { name: "delta", data: { text: "вет**" } },
        { name: "done", data: { text: "**Привет**", message_id: 42 } },
      ]);
    }
    if (request.path.startsWith("/api/history?")) return jsonResponse({ items: [], next_before_id: null });
    if (request.path === "/api/history/42/feedback") return jsonResponse({ ok: true, value: -1 });
    return fallback();
  });
  await openTab(harness, "chat");
  const content = harness.element("content");
  const input = content.querySelector(".chat-input");
  input.value = "вопрос";
  input.dispatchEvent({ type: "input" });
  findButton(content, "Отправить").click();
  await harness.flush();
  await harness.flush();

  const message = harness.requests.find((request) => request.path === "/api/messages");
  assert.ok(message);
  assert.deepEqual(JSON.parse(message.options.body), { text: "вопрос" });
  const article = findArticle(harness.element("content"), "Привет");
  assert.equal(article.querySelectorAll("strong").length, 1);
  assert.equal(findButton(article, "👍").getAttribute("aria-pressed"), "false");
  assert.equal(findButton(article, "👎").getAttribute("aria-pressed"), "false");

  findButton(article, "👎").click();
  await harness.flush();
  const feedback = harness.requests.find((request) => request.path === "/api/history/42/feedback");
  assert.ok(feedback);
  assert.deepEqual(JSON.parse(feedback.options.body), { value: -1 });
  assert.equal(findButton(article, "👎").getAttribute("aria-pressed"), "true");
});
