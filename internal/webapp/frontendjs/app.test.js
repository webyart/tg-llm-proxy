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

test("menu button boot exchanges signed initData with an empty launch ticket", async () => {
  const harness = await createHarness({ hash: "" });
  assert.equal(harness.element("app").hidden, false);
  assert.equal(harness.element("launch").hidden, true);
  assert.equal(harness.requests.length, 2);
  const exchange = harness.requests[0];
  assert.equal(exchange.path, "/api/session");
  assert.equal(exchange.options.method, "POST");
  assert.equal(exchange.options.body, "signed-init-data");
  assert.equal(exchange.options.headers.get("X-Launch-Ticket"), "");
  assert.equal(harness.requests[1].path, "/api/status");
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
  assert.match(results.textContent, /Совпадений больше|оценена только часть/);

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
  assert.equal(harness.requests.length, 0);
  active = findArticle(content, "active reminder");
  findButton(active, "Подтвердить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/reminders/active-1/cancel");
  assert.equal(posts(harness)[0].options.body, undefined);

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


test("history Delete and Clear require inline confirmation and use empty canonical POSTs", async () => {
  const harness = await createHarness();
  await openTab(harness, "history");
  harness.resetRequests();
  const content = harness.element("content");
  let message = findArticle(content, "hello");

  findButton(message, "Удалить").click();
  assert.equal(harness.requests.length, 0);
  message = findArticle(content, "hello");
  findButton(message, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  message = findArticle(content, "hello");
  findButton(message, "Удалить").click();
  message = findArticle(content, "hello");
  findButton(message, "Подтвердить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/history/msg-1/delete");
  assert.equal(posts(harness)[0].options.body, undefined);
  assert.equal(harness.element("notice").textContent, "История обновлена.");

  harness.resetRequests();
  findButton(content, "Очистить историю").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  findButton(content, "Очистить историю").click();
  findButton(content, "Подтвердить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/history/clear");
  assert.equal(posts(harness)[0].options.body, undefined);
});

test("link creation validates inline, sends strict JSON and blocks duplicate submits while busy", async () => {
  const harness = await createHarness();
  await openTab(harness, "links");
  harness.resetRequests();
  const content = harness.element("content");
  const input = content.querySelector('[aria-label="URL"]');
  assert.ok(input);
  for (const invalid of ["javascript:alert(1)", "http://example.test/insecure"]) {
    input.value = invalid;
    input.dispatchEvent({ type: "input" });
    findButton(content, "Добавить").click();
    assert.equal(harness.requests.length, 0);
    assert.equal(input.getAttribute("aria-invalid"), "true");
    assert.equal(harness.element("error").textContent, "Введите корректный HTTPS URL длиной до 2048 байт.");
  }

  let release;
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/links" && request.options.method === "POST") {
      return new Promise((resolve) => { release = () => resolve(jsonResponse({ ok: true, id: 7 })); });
    }
    return fallback();
  });
  input.value = "  https://example.test/a?x=1&y=2  ";
  input.dispatchEvent({ type: "input" });
  const add = findButton(content, "Добавить");
  add.click();
  add.click();
  assert.equal(posts(harness).length, 1);
  assert.equal(add.disabled, true);
  assert.equal(harness.element("loading").hidden, false);
  release();
  await harness.flush();

  const creation = posts(harness)[0];
  assert.equal(creation.options.body, JSON.stringify({ url: "https://example.test/a?x=1&y=2" }));
  assert.equal(creation.options.headers.get("Content-Type"), "application/json");
  assert.equal(creation.options.headers.get("Authorization"), "Bearer test-session");
  assert.equal(harness.element("notice").textContent, "Ссылка добавлена.");
  assert.equal(harness.element("loading").hidden, true);
  assert.ok(content.querySelector('[aria-label="URL"]'));
});

test("link search encodes one query, is read-only and renders at most twenty results", async () => {
  const harness = await createHarness();
  await openTab(harness, "links");
  harness.resetRequests();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/links/search")) {
      return jsonResponse({ items: Array.from({ length: 25 }, (_, index) => ({
        id: index + 1,
        title: `link-${index + 1}`,
        url: `https://example.test/${index + 1}`,
        content: `<unsafe>${index + 1}</unsafe>`,
      })) });
    }
    return fallback();
  });
  const content = harness.element("content");
  const input = content.querySelector('[aria-label="Поиск по ссылкам"]');
  input.value = "  нужное & другое  ";
  input.dispatchEvent({ type: "input" });
  findButton(content, "Найти").click();
  await harness.flush();

  assert.equal(harness.requests.length, 1);
  assert.equal(harness.requests[0].path, "/api/links/search?q=%D0%BD%D1%83%D0%B6%D0%BD%D0%BE%D0%B5%20%26%20%D0%B4%D1%80%D1%83%D0%B3%D0%BE%D0%B5");
  assert.equal(harness.requests[0].options.method, undefined);
  const results = content.querySelector(".search-results");
  assert.equal(results.querySelectorAll("article").length, 20);
  assert.match(results.textContent, /link-20/);
  assert.doesNotMatch(results.textContent, /link-21/);
  assert.match(results.textContent, /<unsafe>1<\/unsafe>/);

  harness.resetRequests();
  const blank = content.querySelector('[aria-label="Поиск по ссылкам"]');
  blank.value = "   ";
  blank.dispatchEvent({ type: "input" });
  findButton(content, "Найти").click();
  assert.equal(harness.requests.length, 0);
  assert.equal(harness.element("error").textContent, "Введите непустой поисковый запрос.");
});

test("once reminder creation sends text, cadence and RFC3339 next_run only", async () => {
  const harness = await createHarness();
  await openTab(harness, "reminders");
  harness.resetRequests();
  const content = harness.element("content");
  const text = content.querySelector('[aria-label="Текст напоминания"]');
  const nextRun = content.querySelector('[aria-label="Дата и время"]');
  text.value = "  Разовый звонок  ";
  text.dispatchEvent({ type: "input" });
  const future = new Date(Date.now() + 24 * 60 * 60 * 1000);
  const localValue = new Date(future.getTime() - future.getTimezoneOffset() * 60 * 1000).toISOString().slice(0, 16);
  nextRun.value = localValue;
  nextRun.dispatchEvent({ type: "input" });
  findButton(content, "Создать").click();
  await harness.flush();

  assert.equal(posts(harness).length, 1);
  const creation = posts(harness)[0];
  assert.equal(creation.path, "/api/reminders");
  assert.deepEqual(JSON.parse(creation.options.body), {
    text: "Разовый звонок",
    cadence: "once",
    next_run: new Date(localValue).toISOString(),
  });
  assert.equal(Object.hasOwn(JSON.parse(creation.options.body), "daily_time"), false);
  assert.equal(creation.options.headers.get("Content-Type"), "application/json");
  assert.equal(harness.element("notice").textContent, "Напоминание создано.");
});

test("daily reminder toggles time access, validates HH:MM and omits next_run", async () => {
  const harness = await createHarness();
  await openTab(harness, "reminders");
  harness.resetRequests();
  const content = harness.element("content");
  content.querySelector('[aria-label="Текст напоминания"]').value = "Ежедневная проверка";
  const cadence = content.querySelector('[aria-label="Периодичность"]');
  cadence.value = "daily";
  cadence.dispatchEvent({ type: "change" });
  const nextRun = content.querySelector('[aria-label="Дата и время"]');
  const dailyTime = content.querySelector('[aria-label="Время ежедневно"]');
  assert.equal(nextRun.disabled, true);
  assert.equal(dailyTime.disabled, false);
  dailyTime.value = "25:00";
  findButton(content, "Создать").click();
  assert.equal(harness.requests.length, 0);
  assert.equal(dailyTime.getAttribute("aria-invalid"), "true");

  dailyTime.value = "09:35";
  dailyTime.dispatchEvent({ type: "input" });
  findButton(content, "Создать").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.deepEqual(JSON.parse(posts(harness)[0].options.body), {
    text: "Ежедневная проверка",
    cadence: "daily",
    daily_time: "09:35",
  });
  assert.equal(Object.hasOwn(JSON.parse(posts(harness)[0].options.body), "next_run"), false);
});

test("task creation validates goal and posts only goal to the lifecycle endpoint", async () => {
  const harness = await createHarness();
  await openTab(harness, "tasks");
  harness.resetRequests();
  const content = harness.element("content");
  const goal = content.querySelector('[aria-label="Цель задачи"]');
  assert.ok(goal);
  findButton(content, "Создать").click();
  assert.equal(harness.requests.length, 0);
  assert.equal(goal.getAttribute("aria-invalid"), "true");
  assert.equal(harness.element("error").textContent, "Введите цель от 1 до 2000 символов без NUL.");

  goal.value = "  Подготовить обзор <безопасно>  ";
  goal.dispatchEvent({ type: "input" });
  findButton(content, "Создать").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/tasks");
  assert.equal(posts(harness)[0].options.body, JSON.stringify({ goal: "Подготовить обзор <безопасно>" }));
  assert.equal(harness.element("notice").textContent, "Задача создана.");
  assert.ok(content.querySelector(".creation-panel"));
});

test("active task Cancel arms and cancels locally before one empty POST", async () => {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/tasks?")) {
      return jsonResponse({ items: [{
        id: 9, goal: "active goal", status: "running", next_step: 1, attempts: 0,
        due_at: "2099-01-01", expires_at: "2099-01-02", created_at: "2026-01-01",
      }] });
    }
    return fallback();
  });
  await openTab(harness, "tasks");
  harness.resetRequests();
  const content = harness.element("content");
  let task = findArticle(content, "active goal");
  findButton(task, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  task = findArticle(content, "active goal");
  findButton(task, "Отменить").click();
  assert.equal(harness.requests.length, 0);
  task = findArticle(content, "active goal");
  findButton(task, "Отменить").click();
  task = findArticle(content, "active goal");
  findButton(task, "Подтвердить").click();
  await harness.flush();
  assert.equal(posts(harness).length, 1);
  assert.equal(posts(harness)[0].path, "/api/tasks/cancel");
  assert.equal(posts(harness)[0].options.body, undefined);
});

test("tablist supports Arrow and Home keys with selected tab and labelled panel", async () => {
  const harness = await createHarness();
  const status = harness.element("tab-status");
  status.dispatchEvent({ type: "keydown", key: "ArrowRight", bubbles: true });
  await harness.flush();
  const history = harness.element("tab-history");
  assert.equal(harness.document.activeElement, history);
  assert.equal(history.getAttribute("aria-selected"), "true");
  assert.equal(status.getAttribute("aria-selected"), "false");
  assert.equal(history.tabIndex, 0);
  assert.equal(status.tabIndex, -1);
  assert.equal(harness.element("content").getAttribute("aria-labelledby"), "tab-history");
  assert.equal(harness.element("section-title").textContent, "История");

  history.dispatchEvent({ type: "keydown", key: "Home", bubbles: true });
  await harness.flush();
  assert.equal(harness.document.activeElement, status);
  assert.equal(status.getAttribute("aria-selected"), "true");
  assert.equal(harness.element("content").getAttribute("aria-labelledby"), "tab-status");
});

test("RU and EN switch translates creation forms and navigation without losing controls", async () => {
  const harness = await createHarness();
  await openTab(harness, "links");
  const language = harness.element("language");
  language.value = "en";
  language.dispatchEvent({ type: "change" });
  assert.equal(harness.document.documentElement.lang, "en");
  assert.match(harness.element("content").textContent, /Add a link/);
  assert.ok(harness.element("content").querySelector('[aria-label="Search links"]'));
  assert.ok(findButton(harness.element("content"), "Add"));

  language.value = "ru";
  language.dispatchEvent({ type: "change" });
  assert.equal(harness.document.documentElement.lang, "ru");
  assert.match(harness.element("content").textContent, /Добавить ссылку/);
  assert.ok(harness.element("content").querySelector('[aria-label="Поиск по ссылкам"]'));
  assert.ok(findButton(harness.element("content"), "Добавить"));
});

test("static frontend regressions enforce local assets, responsive rules, reduced motion and safe DOM APIs", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const script = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
  const html = fs.readFileSync(path.join(__dirname, "../static/index.html"), "utf8");
  const css = fs.readFileSync(path.join(__dirname, "../static/style.css"), "utf8");
  for (const forbidden of ["innerHTML", "outerHTML", "insertAdjacentHTML", "window.confirm", "document.write", "eval("]) {
    assert.equal(script.includes(forbidden), false, forbidden);
  }
  for (const endpoint of ["/api/history/clear", "/api/links/search?q=", "/api/links\",", "/api/reminders\",", "/api/tasks\","]) {
    assert.ok(script.includes(endpoint), endpoint);
  }
  assert.match(html, /role="tablist"/);
  assert.match(html, /aria-live="polite"/);
  assert.match(html, /src="\/app\.js"/);
  assert.match(html, /href="\/style\.css"/);
  assert.doesNotMatch(html, /(?:src|href)="https?:\/\//);
  for (const rule of ["position: sticky", "grid-template-columns", ":focus-visible", "@media (max-width: 420px)", "@media (prefers-reduced-motion: reduce)", ".creation-panel", ".badge"]) {
    assert.ok(css.includes(rule), rule);
  }
  assert.equal(css.includes("overflow-x: auto"), false);
  assert.equal(css.includes("white-space: nowrap"), false);
});

test("every literal translation and static catalog key exists in both RU and EN", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const script = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
  const html = fs.readFileSync(path.join(__dirname, "../static/index.html"), "utf8");
  const catalogs = script.match(/\n    ru: \{([\s\S]*?)\n    \},\n    en: \{([\s\S]*?)\n    \},\n  \};/);
  assert.ok(catalogs);
  const keys = new Set();
  for (const match of script.matchAll(/\bt\("([a-zA-Z][a-zA-Z0-9_]*)"/g)) keys.add(match[1]);
  for (const match of html.matchAll(/data-i18n(?:-aria)?="([a-zA-Z]+)"/g)) keys.add(match[1]);
  const errorMap = script.match(/const errorKeys = \{([\s\S]*?)\n  \};/);
  assert.ok(errorMap);
  for (const match of errorMap[1].matchAll(/:\s*"([a-zA-Z][a-zA-Z0-9_]*)"/g)) keys.add(match[1]);
  assert.ok(keys.size > 100);
  for (const key of keys) {
    assert.ok(catalogs[1].includes(`${key}:`), `RU misses ${key}`);
    assert.ok(catalogs[2].includes(`${key}:`), `EN misses ${key}`);
  }
});

test("miniapp base prefixes session, central API and history export fetches", async () => {
  const harness = await createHarness({ base: "/miniapp" });
  assert.deepEqual(harness.requests.map((request) => request.path), [
    "/miniapp/api/session",
    "/miniapp/api/status",
  ]);

  await openTab(harness, "history");
  harness.resetRequests();
  findButton(harness.element("content"), "Скачать JSON").click();
  await harness.flush();

  assert.equal(harness.requests.length, 1);
  assert.equal(harness.requests[0].path, "/miniapp/api/history/export?format=json");
  assert.equal(harness.requests[0].options.headers.get("Authorization"), "Bearer test-session");
});
