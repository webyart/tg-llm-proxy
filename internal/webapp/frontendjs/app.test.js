"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const {
  blobResponse,
  createHarness,
  findArticle,
  findButton,
  jsonResponse,
  openTab,
} = require("./harness.js");

function posts(harness) {
  return harness.requests.filter((request) => request.options.method === "POST");
}

test("initial boot exchanges the launch and renders status without exceptions", async () => {
  const harness = await createHarness();
  assert.deepEqual(harness.telegramCalls, ["ready", "expand"]);
  assert.equal(harness.element("app").hidden, false);
  assert.equal(harness.element("launch").hidden, true);
  assert.equal(harness.element("logout").hidden, false);
  assert.equal(harness.element("error").hidden, true);
  assert.equal(harness.document.documentElement.lang, "ru");
  assert.equal(harness.document.title, "Бот — компаньон");
  assert.match(harness.element("content").textContent, /Последняя активность|Сообщения/);
  assert.equal(harness.requests.length, 2);
  const exchange = harness.requests[0];
  assert.equal(exchange.path, "/api/session");
  assert.equal(exchange.options.method, "POST");
  assert.equal(exchange.options.body, "signed-init-data");
  assert.equal(exchange.options.headers.get("X-Launch-Ticket"), "ticket-1");
  assert.equal(exchange.options.headers.get("Content-Type"), "text/plain");
  assert.equal(harness.requests[1].path, "/api/status");
  assert.equal(harness.requests[1].options.headers.get("Authorization"), "Bearer test-session");
});

test("document Delete arms and cancels without fetch, then Confirm sends one empty POST despite double-click", async () => {
  const harness = await createHarness();
  await openTab(harness, "documents");
  harness.resetRequests();
  const content = harness.element("content");

  findButton(content, "Удалить").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  assert.equal(content.querySelectorAll("button").filter((button) => button.textContent === "Подтвердить").length, 0);

  let release;
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/documents/doc-1/delete") {
      return new Promise((resolve) => { release = () => resolve(jsonResponse({})); });
    }
    return fallback();
  });
  findButton(content, "Удалить").click();
  assert.equal(harness.requests.length, 0);
  const confirm = findButton(content, "Подтвердить");
  confirm.click();
  confirm.click();
  assert.equal(posts(harness).length, 1);
  assert.equal(confirm.disabled, true);
  assert.equal(harness.element("loading").hidden, false);
  assert.equal(harness.element("content").getAttribute("aria-busy"), "true");
  release();
  await harness.flush();

  assert.equal(posts(harness).length, 1);
  assert.equal(harness.requests[0].path, "/api/documents/doc-1/delete");
  assert.equal(harness.requests[0].options.body, undefined);
  assert.equal(harness.element("loading").hidden, true);
  assert.equal(harness.element("content").getAttribute("aria-busy"), "false");
  assert.equal(harness.element("notice").textContent, "Документы удалены.");
  assert.equal(harness.element("notice").hidden, false);
  assert.equal(harness.element("error").hidden, true);
});

test("document Clear arms and cancels without fetch, then Confirm sends exactly one empty POST", async () => {
  const harness = await createHarness();
  await openTab(harness, "documents");
  harness.resetRequests();
  const content = harness.element("content");

  findButton(content, "Очистить документы").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Очистить документы").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Подтвердить").click();
  await harness.flush();

  const mutations = posts(harness);
  assert.equal(mutations.length, 1);
  assert.equal(mutations[0].path, "/api/documents/clear");
  assert.equal(mutations[0].options.body, undefined);
  assert.equal(mutations[0].options.headers.get("Authorization"), "Bearer test-session");
});

test("document search is read-only, busy-safe, bounded to three results and validates blank queries", async () => {
  const harness = await createHarness();
  await openTab(harness, "documents");
  harness.resetRequests();
  const content = harness.element("content");
  const input = content.querySelector("input");
  input.value = "  need & more  ";
  input.dispatchEvent({ type: "input" });

  let release;
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/documents/search")) {
      return new Promise((resolve) => {
        release = () => resolve(jsonResponse({
          items: Array.from({ length: 4 }, (_, index) => ({
            document_id: `result-${index + 1}`,
            name: `name-${index + 1}`,
            score: 0.9,
            content: `match-${index + 1}`,
          })),
          corpus: { truncated: true },
        }));
      });
    }
    return fallback();
  });
  const search = findButton(content, "Найти");
  search.click();
  search.click();
  assert.equal(harness.requests.length, 1);
  assert.equal(search.disabled, true);
  release();
  await harness.flush();

  assert.equal(harness.requests[0].path, "/api/documents/search?q=need%20%26%20more");
  assert.equal(harness.requests[0].options.method, undefined);
  const results = content.querySelector(".search-results");
  assert.equal(results.querySelectorAll("article").length, 3);
  assert.match(results.textContent, /result-3/);
  assert.doesNotMatch(results.textContent, /result-4/);
  assert.match(results.textContent, /Поиск охватил только часть/);

  harness.resetRequests();
  const blank = content.querySelector("input");
  blank.value = "   ";
  blank.dispatchEvent({ type: "input" });
  findButton(content, "Найти").click();
  assert.equal(harness.requests.length, 0);
  assert.equal(harness.element("error").textContent, "Введите непустой поисковый запрос.");
  assert.equal(harness.element("notice").hidden, true);
});

test("reminder controls are scoped by state and use the expected empty POST actions", async () => {
  const harness = await createHarness();
  await openTab(harness, "reminders");
  harness.resetRequests();
  const content = harness.element("content");
  let active = findArticle(content, "active reminder");
  let failed = findArticle(content, "failed reminder");
  assert.deepEqual(active.querySelectorAll("button").map((button) => button.textContent), ["Отменить"]);
  assert.deepEqual(failed.querySelectorAll("button").map((button) => button.textContent), ["Повторить", "Удалить"]);

  findButton(failed, "Удалить").click();
  assert.equal(harness.requests.length, 0);
  failed = findArticle(content, "failed reminder");
  findButton(failed, "Отменить").click();
  assert.equal(harness.requests.length, 0);

  failed = findArticle(content, "failed reminder");
  findButton(failed, "Повторить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/reminders/failed-1/retry");
  assert.equal(posts(harness)[0].options.body, undefined);
  assert.equal(harness.element("notice").textContent, "Напоминание обновлено.");

  harness.resetRequests();
  active = findArticle(content, "active reminder");
  findButton(active, "Отменить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/reminders/active-1/cancel");

  harness.resetRequests();
  failed = findArticle(content, "failed reminder");
  findButton(failed, "Удалить").click();
  assert.equal(harness.requests.length, 0);
  failed = findArticle(content, "failed reminder");
  findButton(failed, "Подтвердить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/reminders/failed-1/delete");
  assert.equal(posts(harness)[0].options.body, undefined);
});

test("mutation errors replace success notices and restore controls", async () => {
  const harness = await createHarness();
  await openTab(harness, "reminders");
  harness.resetRequests();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/reminders/failed-1/retry") {
      return jsonResponse({ error: { code: "reminder_mutation_failed" } }, 500);
    }
    return fallback();
  });
  const failed = findArticle(harness.element("content"), "failed reminder");
  findButton(failed, "Повторить").click();
  await harness.flush();

  assert.equal(harness.element("error").textContent, "Не удалось изменить напоминание.");
  assert.equal(harness.element("error").hidden, false);
  assert.equal(harness.element("notice").hidden, true);
  assert.equal(harness.element("notice").textContent, "");
  assert.equal(harness.element("loading").hidden, true);
  assert.equal(harness.element("refresh").disabled, false);
});

test("401 expires the session and prevents later API loads", async () => {
  const harness = await createHarness();
  harness.resetRequests();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/status") return jsonResponse({ error: { code: "session_expired" } }, 401);
    return fallback();
  });
  harness.element("refresh").click();
  await harness.flush();

  assert.equal(harness.element("app").hidden, true);
  assert.equal(harness.element("launch").hidden, false);
  assert.equal(harness.element("logout").hidden, true);
  assert.equal(harness.element("error").textContent, "Сессия истекла. Откройте Mini App заново из Telegram.");
  harness.resetRequests();
  harness.element("refresh").click();
  await harness.flush();
  assert.equal(harness.requests.length, 0);
});

test("429 shows the localized rate-limit error without expiring the session", async () => {
  const harness = await createHarness();
  harness.resetRequests();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/status") return jsonResponse({ error: { code: "overloaded" } }, 429);
    return fallback();
  });
  harness.element("refresh").click();
  await harness.flush();

  assert.equal(harness.element("error").textContent, "Слишком много запросов. Повторите попытку позже.");
  assert.equal(harness.element("error").hidden, false);
  assert.equal(harness.element("app").hidden, false);
  assert.equal(harness.element("launch").hidden, true);
});

test("history export errors never start a download or create an object URL", async () => {
  const harness = await createHarness();
  await openTab(harness, "history");
  harness.resetRequests();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/history/export?format=md") {
      return jsonResponse({ error: { code: "export_failed" } }, 500);
    }
    return fallback();
  });
  findButton(harness.element("content"), "Скачать Markdown").click();
  await harness.flush();

  assert.equal(harness.requests.length, 1);
  assert.equal(harness.document.downloads.length, 0);
  assert.equal(harness.createdObjectURLs.length, 0);
  assert.equal(harness.revokedObjectURLs.length, 0);
  assert.equal(harness.element("error").textContent, "Не удалось экспортировать историю.");
  assert.equal(harness.element("notice").hidden, true);
});

test("successful history export clicks one temporary anchor and revokes its object URL", async () => {
  const harness = await createHarness();
  await openTab(harness, "history");
  harness.resetRequests();
  const blob = { size: 5 };
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/history/export?format=json") return blobResponse(blob);
    return fallback();
  });
  findButton(harness.element("content"), "Скачать JSON").click();
  await harness.flush();

  assert.equal(harness.createdObjectURLs.length, 1);
  assert.equal(harness.createdObjectURLs[0].blob, blob);
  assert.deepEqual(harness.revokedObjectURLs, [harness.createdObjectURLs[0].value]);
  assert.equal(harness.document.downloads.length, 1);
  const download = harness.document.downloads[0];
  assert.equal(download.download, "history.json");
  assert.equal(download.href, harness.createdObjectURLs[0].value);
  assert.equal(download.element.parentNode, null);
  assert.equal(harness.requests[0].options.headers.get("Authorization"), "Bearer test-session");
  assert.equal(harness.element("notice").textContent, "Экспорт истории подготовлен.");
  assert.equal(harness.element("error").hidden, true);
});

test("TTS speed save and reset use JSON PUT and empty DELETE with dedicated notices", async () => {
  const harness = await createHarness();
  await openTab(harness, "settings");
  harness.resetRequests();
  const content = harness.element("content");
  let speed = content.querySelector('[aria-label="Скорость озвучивания"]');
  assert.deepEqual(speed.querySelectorAll("option").map((option) => option.value), ["0.75", "1", "1.25", "1.5"]);
  assert.equal(speed.querySelectorAll("option").find((option) => option.selected).value, "1");
  speed.value = "1.5";
  findButton(content, "Сохранить скорость").click();
  await harness.flush();

  const update = harness.requests.find((request) => request.path === "/api/settings/tts-speed");
  assert.equal(update.options.method, "PUT");
  assert.equal(update.options.body, JSON.stringify({ speed: "1.5" }));
  assert.equal(update.options.headers.get("Content-Type"), "application/json");
  assert.equal(harness.element("notice").textContent, "Скорость озвучивания сохранена.");
  assert.equal(harness.element("error").hidden, true);

  harness.resetRequests();
  speed = content.querySelector('[aria-label="Скорость озвучивания"]');
  assert.ok(speed);
  findButton(content, "Сбросить скорость").click();
  await harness.flush();
  const reset = harness.requests.find((request) => request.path === "/api/settings/tts-speed");
  assert.equal(reset.options.method, "DELETE");
  assert.equal(reset.options.body, undefined);
  assert.equal(harness.element("notice").textContent, "Скорость озвучивания сброшена.");
  assert.equal(harness.element("error").hidden, true);
});
