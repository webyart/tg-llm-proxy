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
  assert.equal(harness.element("tab-chat").getAttribute("aria-selected"), "true");
  assert.equal(harness.requests.length, 4);
  const exchange = harness.requests[0];
  assert.equal(exchange.path, "/api/session");
  assert.equal(exchange.options.method, "POST");
  assert.equal(exchange.options.body, "signed-init-data");
  assert.equal(exchange.options.headers.get("X-Launch-Ticket"), "ticket-1");
  assert.equal(exchange.options.headers.get("Content-Type"), "text/plain");
  assert.equal(harness.requests[1].path, "/api/status");
  assert.equal(harness.requests[2].path, "/api/history?limit=20");
  assert.equal(harness.requests[3].path, "/api/whoami");
  assert.equal(harness.requests[1].options.headers.get("Authorization"), "Bearer test-session");
});

test("menu button boot exchanges signed initData with an empty launch ticket", async () => {
  const harness = await createHarness({ hash: "" });
  assert.equal(harness.element("app").hidden, false);
  assert.equal(harness.element("launch").hidden, true);
  assert.equal(harness.requests.length, 4);
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
  const input = content.querySelector('input[type="search"]');
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
  const blank = content.querySelector('input[type="search"]');
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
    if (request.path === "/api/status") return jsonResponse({ error: { code: "rate_limited" } }, 429);
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


test("Mini App fallback toggle saves the chat override and resets it", async () => {
  const harness = await createHarness();
  await openTab(harness, "settings");
  harness.resetRequests();
  const content = harness.element("content");
  let toggle = content.querySelector('[aria-label="Доставка ответов Mini App в Telegram"]');
  assert.ok(toggle);
  assert.ok(toggle.classList.contains("settings-toggle"));
  assert.equal(toggle.getAttribute("aria-pressed"), "true");
  assert.equal(toggle.textContent, "Включён");
  toggle.click();
  assert.equal(toggle.getAttribute("aria-pressed"), "false");
  assert.equal(toggle.textContent, "Выключен");
  findButton(content, "Сохранить доставку в Telegram").click();
  await harness.flush();

  const update = harness.requests.find((request) => request.path === "/api/settings/miniapp-fallback");
  assert.equal(update.options.method, "PUT");
  assert.equal(update.options.body, JSON.stringify({ enabled: false }));
  assert.equal(update.options.headers.get("Content-Type"), "application/json");
  assert.equal(harness.element("notice").textContent, "Доставка ответов Mini App в Telegram сохранена.");
  assert.equal(harness.element("error").hidden, true);

  harness.resetRequests();
  toggle = content.querySelector('[aria-label="Доставка ответов Mini App в Telegram"]');
  assert.ok(toggle);
  findButton(content, "Сбросить доставку в Telegram").click();
  await harness.flush();
  const reset = harness.requests.find((request) => request.path === "/api/settings/miniapp-fallback");
  assert.equal(reset.options.method, "DELETE");
  assert.equal(reset.options.body, undefined);
  assert.equal(harness.element("notice").textContent, "Доставка ответов Mini App в Telegram сброшена.");
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
  assert.doesNotMatch(html, /aria-live=/);
  assert.equal((script.match(/setAttribute\("aria-live"/g) || []).length, 1);
  assert.match(script, /log\.setAttribute\("role", "log"\)/);
  const uncommentedCSS = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const cssBodies = (selector) => {
    const bodies = [];
    const rule = /^[ \t]*([^{}\n][^{}]*?)[ \t]*\{([^}]*)\}/gm;
    for (const match of uncommentedCSS.matchAll(rule)) {
      const selectors = match[1].split(",").map((value) => value.trim());
      if (selectors.some((value) => value.startsWith("@"))) continue;
      if (selectors.includes(selector)) bodies.push(match[2]);
    }
    return bodies;
  };
  const assertHeight = (selector) => {
    const bodies = cssBodies(selector);
    assert.ok(bodies.length > 0, `${selector} has no CSS rule`);
    const heights = [...bodies.join("\n").matchAll(/min-height:\s*(\d+)px/g)].map((match) => Number(match[1]));
    assert.ok(heights.length > 0, `${selector} declares no min-height`);
    assert.ok(heights.every((value) => value >= 44), `${selector} declares a sub-44 px height`);
  };
  for (const selector of [
    "button", "button.secondary", ".menu-toggle", ".logout-button", ".refresh-button",
    "#more.load-more", ".language-select", ".account-controls .language-select",
    "input", "select", "textarea", "#tabs.menu-panel button",
    '.document-upload-row input[type="file"]', ".document-upload-row button",
    ".settings-toggle", ".chat-actions button", ".chat-send", ".chat-mic", ".chat-voice button",
    ".item > a",
  ]) {
    assertHeight(selector);
  }
  for (const selector of [
    "button", ".menu-toggle", ".settings-toggle", ".chat-send", ".chat-mic", ".chat-voice button",
    "#tabs.menu-panel button", ".document-upload-row button", ".account-controls .language-select",
    ".settings-row > button", ".task-actions > button", ".form-actions > button", ".item > a",
  ]) {
    const bodies = cssBodies(selector);
    assert.ok(bodies.length > 0, `${selector} has no CSS rule`);
    const widths = [...bodies.join("\n").matchAll(/min-width:\s*(\d+)px/g)].map((match) => Number(match[1]));
    assert.ok(widths.length > 0, `${selector} declares no min-width`);
    assert.ok(widths.every((value) => value >= 44), `${selector} declares a sub-44 px width`);
  }
  // Explicit exemption: inline prose links (a) are covered by the WCAG 2.5.5 inline exception.
  assert.match(css, /WCAG 2\.5\.5 exempts targets in a sentence/);
  assert.match(css, /\.item > a \{[^}]*min-width: 44px;[^}]*min-height: 44px;/);
  assert.match(css, /#tabs\.menu-panel button \{[^}]*min-width: 44px;[^}]*min-height: 44px;/);
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

test("every server error code written by writeError is mapped in errorKeys", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const root = path.join(__dirname, "..");
  const script = fs.readFileSync(path.join(root, "static/app.js"), "utf8");
  const errorMap = script.match(/const errorKeys = \{([\s\S]*?)\n  \};/);
  assert.ok(errorMap, "errorKeys map is missing");
  const mapped = new Set();
  for (const match of errorMap[1].matchAll(/([a-z][a-z0-9_]*):\s*"[a-zA-Z][a-zA-Z0-9_]*"/g)) mapped.add(match[1]);
  const sources = fs.readdirSync(root).filter((name) => name.endsWith(".go") && !name.endsWith("_test.go"));
  assert.ok(sources.length > 5, `only ${sources.length} server sources found`);
  const codes = new Set();
  for (const name of sources) {
    const source = fs.readFileSync(path.join(root, name), "utf8");
    for (const match of source.matchAll(/writeError\([^,\n]+,\s*[^,\n]+,\s*"([a-z][a-z0-9_]*)"/g)) codes.add(match[1]);
  }
  assert.ok(codes.size >= 40, `only ${codes.size} writeError codes found`);
  for (const code of codes) assert.ok(mapped.has(code), `errorKeys misses server code ${code}`);
});

test("miniapp base prefixes session, central API and history export fetches", async () => {
  const harness = await createHarness({ base: "/miniapp" });
  assert.deepEqual(harness.requests.map((request) => request.path), [
    "/miniapp/api/session",
    "/miniapp/api/status",
    "/miniapp/api/history?limit=20",
    "/miniapp/api/whoami",
  ]);

  await openTab(harness, "history");
  harness.resetRequests();
  findButton(harness.element("content"), "Скачать JSON").click();
  await harness.flush();

  assert.equal(harness.requests.length, 1);
  assert.equal(harness.requests[0].path, "/miniapp/api/history/export?format=json");
  assert.equal(harness.requests[0].options.headers.get("Authorization"), "Bearer test-session");
});

function enableSvg(harness) {
  harness.document.createElementNS = (namespace, tag) => {
    assert.equal(namespace, "http://www.w3.org/2000/svg");
    return harness.document.createElement(tag);
  };
}

function usageSeries(count) {
  const base = Date.UTC(2026, 2, 2, 0, 0, 0);
  return Array.from({ length: count }, (_, index) => ({
    start: new Date(base - (count - 1 - index) * 86400000).toISOString(),
    model_calls: index % 3 === 0 ? index + 1 : 0,
    tokens_in: index * 10,
    tokens_out: index * 20,
    tools: {
      read_url: index % 2 === 0 ? 1 : 0,
      web_search: index > 1 ? 1 : 0,
      calculator: 0,
      rag: index > 3 ? 1 : 0,
    },
    tools_total: (index % 2 === 0 ? 1 : 0) + (index > 1 ? 1 : 0) + (index > 3 ? 1 : 0),
    cost_usd: index * 0.001,
  }));
}

function usagePayload(range) {
  const series = usageSeries(range === "week" ? 8 : 7);
  return {
    date: "2026-03-02",
    models: [{ model: "series-model", calls: 2, tokens_in: 7, tokens_out: 9, cost_usd: 0.000123 }],
    tools: { read_url: 1, web_search: 2, calculator: 3, rag: 4 },
    limits: {
      model: { used: 2, limit: 10, remaining: 8 },
      network_tools: { used: 1, limit: 5, remaining: 4 },
    },
    range,
    truncated: false,
    series,
    totals: {
      model_calls: 2,
      tokens_in: 7,
      tokens_out: 9,
      tool_calls: 10,
      cost_usd: 1.23,
      models: [{ model: "series-model", calls: 2, tokens_in: 7, tokens_out: 9, cost_usd: 0.000123 }],
      tools: { read_url: 1, web_search: 2, calculator: 3, rag: 4 },
    },
  };
}

test("usage tab requests the selected range and renders accessible inline SVG charts", async () => {
  const harness = await createHarness();
  enableSvg(harness);
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/usage")) {
      const range = new URL(request.path, "https://example.test").searchParams.get("range") || "day";
      return jsonResponse(usagePayload(range === "week" ? "week" : "day"));
    }
    return fallback();
  });
  harness.resetRequests();
  await openTab(harness, "usage");
  assert.equal(harness.requests.length, 1);
  assert.equal(harness.requests[0].path, "/api/usage?range=day");
  assert.equal(harness.requests[0].options.headers.get("Authorization"), "Bearer test-session");

  const content = harness.element("content");
  const labels = content.querySelectorAll("button").map((button) => button.textContent);
  for (const label of ["Час", "День", "Неделя", "Месяц"]) {
    assert.ok(labels.includes(label), `range button ${label} is missing`);
  }
  const pressed = content.querySelectorAll("button")
    .filter((button) => button.getAttribute("aria-pressed") === "true")
    .map((button) => button.textContent);
  assert.deepEqual(pressed, ["День"]);

  const svgs = content.querySelectorAll("svg");
  assert.equal(svgs.length, 4);
  for (const svg of svgs) {
    assert.equal(svg.getAttribute("role"), "img");
    assert.ok((svg.getAttribute("aria-label") || "").length > 20);
    assert.match(svg.getAttribute("viewBox"), /^0 0 320 \d+$/);
  }
  assert.ok(content.querySelectorAll("rect").length >= 5);
  assert.ok(content.querySelectorAll("polyline").length >= 2);
  assert.ok(content.textContent.includes("series-model"));
  assert.ok(content.textContent.includes("Входящие"));
  assert.ok(content.textContent.includes("Исходящие"));
  assert.ok(content.textContent.includes("$1.23"));
  assert.ok(content.textContent.includes("Стоимость"));

  const weekButton = content.querySelectorAll("button").find((button) => button.textContent === "Неделя");
  weekButton.click();
  await harness.flush();
  assert.equal(harness.requests.at(-1).path, "/api/usage?range=week");
  const after = harness.element("content");
  const pressedAfter = after.querySelectorAll("button")
    .filter((button) => button.getAttribute("aria-pressed") === "true")
    .map((button) => button.textContent);
  assert.deepEqual(pressedAfter, ["Неделя"]);
  assert.equal(after.querySelectorAll("svg").length, 4);
});

test("usage tab tolerates the pre-range response without series or totals", async () => {
  const harness = await createHarness();
  enableSvg(harness);
  harness.resetRequests();
  await openTab(harness, "usage");
  assert.equal(harness.requests[0].path, "/api/usage?range=day");
  const content = harness.element("content");
  assert.ok(content.textContent.includes("Обращений за сегодня не было."));
  assert.ok(content.textContent.includes("За выбранный период обращений не было."));
  assert.equal(content.querySelectorAll("svg").length, 1);
});

function adminChatFixture(overrides = {}) {
  return {
    id: 11,
    title: "Личный чат",
    type: "private",
    user_id: 11,
    last_activity: "2026-01-01 10:00:00",
    today: { model_calls: 3, read_url: 1, web_search: 2, calculator: 0, cost_usd: 0.5 },
    limits: { daily_model_limit: 10, daily_network_limit: 5, per_minute_limit: 2, daily_cost_limit_usd: 25, disabled_tools: ["web_search"] },
    has_limits: true,
    ...overrides,
  };
}

function adminSettingsHandler(chats, extra = {}) {
  return async (request, fallback) => {
    if (request.path === "/api/admin/chats?limit=50") return jsonResponse({ items: chats, limit: 50 });
    if (extra.handle && extra.handle(request)) return extra.handle(request);
    if (request.path.startsWith("/api/settings")) {
      const response = await fallback();
      const body = await response.json();
      body.is_admin = true;
      return jsonResponse(body);
    }
    return fallback();
  };
}

function limitsSection(harness) {
  const content = harness.element("content");
  const section = content.querySelectorAll("section").find((node) => node.classList.contains("chat-limits"));
  assert.ok(section, "the admin chat-limits section is missing");
  return section;
}

test("non-admin settings has no chat-limits section and makes no admin request", async () => {
  const harness = await createHarness();
  harness.resetRequests();
  await openTab(harness, "settings");
  const content = harness.element("content");
  assert.equal(content.textContent.includes("Лимиты чатов"), false);
  assert.equal(content.textContent.includes("Провайдеры и модели"), false);
  assert.equal(harness.requests.some((request) => request.path.startsWith("/api/admin/")), false);
});

test("admin chat limits render usage, edit and save the strict payload", async () => {
  const harness = await createHarness();
  const chats = [
    adminChatFixture(),
    adminChatFixture({ id: -100200, title: "Рабочая группа", type: "group" }),
  ];
  let savedPath = "";
  let savedBody = null;
  harness.setHandler(adminSettingsHandler(chats, {
    handle(request) {
      if (request.path === "/api/admin/chats/11/limits" && request.options.method === "PUT") {
        savedPath = request.path;
        savedBody = JSON.parse(request.options.body);
        return jsonResponse({ ok: true, id: 11 });
      }
      return null;
    },
  }));
  harness.resetRequests();
  await openTab(harness, "settings");
  const section = limitsSection(harness);
  assert.ok(section.textContent.includes("Сегодня (UTC): модель 3, сеть 3, read_url 1, web_search 2, calculator 0"));
  const choice = section.querySelectorAll("select");
  assert.equal(choice.length, 1);
  assert.equal(choice[0].getAttribute("aria-label"), "Чат");
  assert.equal(choice[0].querySelectorAll("option").length, 2);
  assert.ok(choice[0].querySelectorAll("option")[0].textContent.includes("11 · Личный чат · личный"));
  assert.ok(choice[0].querySelectorAll("option")[1].textContent.includes("-100200 · Рабочая группа · группа"));

  assert.ok(section.textContent.includes("Расход за сегодня (UTC): $0.50 из $25.00"));
  const numbers = section.querySelectorAll("input").filter((input) => input.type === "number");
  assert.deepEqual(numbers.map((input) => input.value), ["10", "5", "2", "25"]);
  assert.equal(numbers[3].step, "0.01");
  assert.equal(numbers[3].max, "100000");
  const boxes = section.querySelectorAll("input").filter((input) => input.type === "checkbox");
  assert.deepEqual(boxes.map((box) => box.checked), [false, true, false]);

  numbers[0].value = "20";
  numbers[1].value = "6";
  numbers[2].value = "3";
  numbers[3].value = "2.5";
  boxes[0].checked = true;
  findButton(section, "Сохранить").click();
  await harness.flush();

  assert.equal(savedPath, "/api/admin/chats/11/limits");
  assert.deepEqual(savedBody, {
    daily_model_limit: 20, daily_network_limit: 6, per_minute_limit: 3,
    daily_cost_limit_usd: 2.5,
    disabled_tools: ["read_url", "web_search"],
  });
  assert.ok(harness.element("notice").textContent.includes("Лимиты чата сохранены."));
  assert.ok(harness.requests.filter((request) => request.path === "/api/admin/chats?limit=50").length >= 2);
});

test("admin chat limits switch chat and reset one row", async () => {
  const harness = await createHarness();
  const chats = [
    adminChatFixture(),
    adminChatFixture({
      id: -100200, title: "Рабочая группа", type: "group",
      today: { model_calls: 7, read_url: 0, web_search: 1, calculator: 2, cost_usd: 0 },
      limits: { daily_model_limit: 0, daily_network_limit: 4, per_minute_limit: 1, daily_cost_limit_usd: 0, disabled_tools: [] },
    }),
  ];
  let deletedPath = "";
  harness.setHandler(adminSettingsHandler(chats, {
    handle(request) {
      if (request.path === "/api/admin/chats/-100200/limits" && request.options.method === "DELETE") {
        deletedPath = request.path;
        return jsonResponse({ ok: true, id: -100200 });
      }
      return null;
    },
  }));
  await openTab(harness, "settings");
  let section = limitsSection(harness);
  const choice = section.querySelectorAll("select")[0];
  choice.value = "-100200";
  choice.dispatchEvent({ type: "change", bubbles: false });
  await harness.flush();

  section = limitsSection(harness);
  assert.ok(section.textContent.includes("модель 7, сеть 1, read_url 0, web_search 1, calculator 2"));
  const numbers = section.querySelectorAll("input").filter((input) => input.type === "number");
  assert.deepEqual(numbers.map((input) => input.value), ["0", "4", "1", "0"]);
  findButton(section, "Сбросить").click();
  await harness.flush();
  assert.equal(deletedPath, "/api/admin/chats/-100200/limits");
  assert.ok(harness.element("notice").textContent.includes("Лимиты чата сброшены."));
});

test("admin chat limits reject a non-integer before calling the API", async () => {
  const harness = await createHarness();
  harness.setHandler(adminSettingsHandler([adminChatFixture()], {
    handle(request) {
      if (request.path.startsWith("/api/admin/chats/") && request.options.method === "PUT") {
        return jsonResponse({ ok: true, id: 11 });
      }
      return null;
    },
  }));
  await openTab(harness, "settings");
  const section = limitsSection(harness);
  const numbers = section.querySelectorAll("input").filter((input) => input.type === "number");
  numbers[0].value = "1.5";
  const before = harness.requests.length;
  findButton(section, "Сохранить").click();
  await harness.flush();
  assert.equal(harness.requests.length, before);
  assert.ok(harness.element("error").textContent.includes("целые числа от 0 до 1000000"));
});

test("admin chat cost budget shows the warning and rejects an invalid amount locally", async () => {
  const harness = await createHarness();
  const chat = adminChatFixture({
    today: { model_calls: 3, read_url: 0, web_search: 0, calculator: 0, cost_usd: 0.8, warning: "chat_cost_budget_warning" },
    limits: { daily_model_limit: 0, daily_network_limit: 0, per_minute_limit: 0, daily_cost_limit_usd: 1, disabled_tools: [] },
  });
  harness.setHandler(adminSettingsHandler([chat], {
    handle(request) {
      if (request.path.startsWith("/api/admin/chats/") && request.options.method === "PUT") {
        return jsonResponse({ ok: true, id: 11 });
      }
      return null;
    },
  }));
  await openTab(harness, "settings");
  const section = limitsSection(harness);
  assert.ok(section.textContent.includes("Расход за сегодня (UTC): $0.80 из $1.00"));
  assert.ok(section.textContent.includes("Израсходовано не менее 80%"));
  const numbers = section.querySelectorAll("input").filter((input) => input.type === "number");
  numbers[3].value = "100000.5";
  const before = harness.requests.length;
  findButton(section, "Сохранить").click();
  await harness.flush();
  assert.equal(harness.requests.length, before);
  assert.ok(harness.element("error").textContent.includes("от 0 до 100000 USD"));
});

test("admin chat list failure is localized inside the section", async () => {
  const harness = await createHarness();
  harness.setHandler(async (request, fallback) => {
    if (request.path === "/api/admin/chats?limit=50") {
      return jsonResponse({ error: { code: "admin_required", message: "Administrator access is required." } }, 403);
    }
    if (request.path.startsWith("/api/settings")) {
      const response = await fallback();
      const body = await response.json();
      body.is_admin = true;
      return jsonResponse(body);
    }
    return fallback();
  });
  await openTab(harness, "settings");
  const section = limitsSection(harness);
  assert.ok(section.textContent.includes("Раздел доступен только администратору."));
});

function adminProviderFixture(overrides = {}) {
  return {
    name: "kimi",
    base_url: "api.moonshot.ai",
    models: ["kimi-k3", "kimi-k2"],
    default_model: "kimi-k3",
    enabled: true,
    source: "env",
    configured_models: ["kimi-k3", "kimi-k2"],
    ...overrides,
  };
}

function providerSection(harness) {
  const content = harness.element("content");
  const section = content.querySelectorAll("section").find((node) => node.classList.contains("provider-settings"));
  assert.ok(section, "the admin provider section is missing");
  return section;
}

test("admin providers render, edit, save and reset the strict payload", async () => {
  const harness = await createHarness();
  let saved = null;
  let resetPath = "";
  harness.setHandler(adminSettingsHandler([], {
    handle(request) {
      if (request.path === "/api/admin/providers") {
        return jsonResponse({ items: [adminProviderFixture()] });
      }
      if (request.path === "/api/admin/providers/kimi" && request.options.method === "PUT") {
        saved = {
          body: JSON.parse(request.options.body),
          contentType: request.options.headers.get("Content-Type"),
        };
        return jsonResponse(adminProviderFixture({ source: "db", models: ["new-a", "new-b"], default_model: "new-b", enabled: false }));
      }
      if (request.path === "/api/admin/providers/kimi" && request.options.method === "DELETE") {
        resetPath = request.path;
        return jsonResponse(adminProviderFixture());
      }
      return null;
    },
  }));
  await openTab(harness, "settings");
  let section = providerSection(harness);
  assert.ok(section.textContent.includes("Провайдеры и модели"));
  assert.ok(section.textContent.includes("host: api.moonshot.ai"));
  assert.ok(section.textContent.includes("env (MODEL_PROVIDERS)"));
  assert.equal(section.querySelector('input[type="checkbox"]').checked, true);
  const textarea = section.querySelector("textarea");
  assert.equal(textarea.value, "kimi-k3\nkimi-k2");
  const select = section.querySelector("select");
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.value), ["kimi-k3", "kimi-k2"]);
  assert.equal(select.value, "kimi-k3");

  textarea.value = "new-a\nnew-b";
  textarea.dispatchEvent({ type: "input" });
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.value), ["new-a", "new-b"]);
  select.value = "new-b";
  section.querySelector('input[type="checkbox"]').checked = false;
  findButton(section, "Сохранить").click();
  await harness.flush();
  assert.deepEqual(saved.body, { models: ["new-a", "new-b"], default_model: "new-b", enabled: false });
  assert.equal(saved.contentType, "application/json");
  assert.ok(harness.element("notice").textContent.includes("Настройки провайдера сохранены."));
  assert.equal(harness.element("error").hidden, true);

  section = providerSection(harness);
  assert.ok(harness.requests.filter((request) => request.path === "/api/admin/providers").length >= 2);
  findButton(section, "Сбросить к env").click();
  await harness.flush();
  assert.equal(resetPath, "/api/admin/providers/kimi");
  assert.ok(harness.element("notice").textContent.includes("Провайдер сброшен к значениям env."));
  assert.equal(harness.element("error").hidden, true);
});

test("admin providers validate models locally before any request", async () => {
  const harness = await createHarness();
  harness.setHandler(adminSettingsHandler([], {
    handle(request) {
      if (request.path === "/api/admin/providers") return jsonResponse({ items: [adminProviderFixture()] });
      return null;
    },
  }));
  await openTab(harness, "settings");
  const section = providerSection(harness);
  harness.resetRequests();
  const textarea = section.querySelector("textarea");
  const select = section.querySelector("select");

  textarea.value = Array.from({ length: 21 }, (_, index) => `model-${index}`).join("\n");
  textarea.dispatchEvent({ type: "input" });
  assert.equal(textarea.getAttribute("aria-invalid"), "true");
  findButton(section, "Сохранить").click();
  await harness.flush();
  assert.equal(harness.requests.length, 0);
  assert.ok(harness.element("error").textContent.includes("1 до 20"));

  textarea.value = "same\nsame";
  textarea.dispatchEvent({ type: "input" });
  findButton(section, "Сохранить").click();
  await harness.flush();
  assert.equal(harness.requests.length, 0);

  textarea.value = "one\ntwo";
  textarea.dispatchEvent({ type: "input" });
  assert.equal(textarea.getAttribute("aria-invalid"), "false");
  assert.equal(select.querySelectorAll("option").length, 2);
  assert.equal(select.value, "one");
});

test("admin provider section switches language without another admin request", async () => {
  const harness = await createHarness();
  harness.setHandler(adminSettingsHandler([], {
    handle(request) {
      if (request.path === "/api/admin/providers") return jsonResponse({ items: [adminProviderFixture()] });
      return null;
    },
  }));
  await openTab(harness, "settings");
  const before = harness.requests.filter((request) => request.path === "/api/admin/providers").length;
  const language = harness.element("language");
  language.value = "en";
  language.dispatchEvent({ type: "change" });
  const section = providerSection(harness);
  assert.ok(section.textContent.includes("Providers and models"));
  assert.ok(section.textContent.includes("Reset to env"));
  assert.equal(harness.requests.filter((request) => request.path === "/api/admin/providers").length, before);
  language.value = "ru";
  language.dispatchEvent({ type: "change" });
  assert.ok(providerSection(harness).textContent.includes("Провайдеры и модели"));
});

// ---- v1.68: provider card contrast and five muted themes -------------------

function styleRules(css) {
  const cleaned = css.replace(/\/\*[\s\S]*?\*\//g, "");
  const rules = [];
  const rule = /^[ \t]*([^{}\n][^{}]*?)[ \t]*\{([^}]*)\}/gm;
  for (const match of cleaned.matchAll(rule)) {
    const selectors = match[1].split(",").map((value) => value.trim());
    if (selectors.some((value) => value.startsWith("@"))) continue;
    rules.push({ selectors, body: match[2] });
  }
  return rules;
}

function styleBodies(rules, selector) {
  return rules.filter((rule) => rule.selectors.includes(selector)).map((rule) => rule.body);
}

function styleBody(rules, selector) {
  const bodies = styleBodies(rules, selector);
  assert.ok(bodies.length > 0, `${selector} has no CSS rule`);
  return bodies.join("\n");
}

function descendants(node) {
  const all = [];
  for (const child of node.childNodes) all.push(child, ...descendants(child));
  return all;
}

test("admin provider cards use theme tokens and keep every default model fully visible", async () => {
  const harness = await createHarness();
  harness.setHandler(adminSettingsHandler([], {
    handle(request) {
      if (request.path === "/api/admin/providers") {
        return jsonResponse({
          items: [adminProviderFixture({
            name: "deepseek",
            base_url: "api.deepseek.com",
            models: ["deepseek-flash", "kimi-k3.1-pro"],
            default_model: "kimi-k3.1-pro",
            configured_models: ["deepseek-flash", "kimi-k3.1-pro"],
          })],
        });
      }
      return null;
    },
  }));
  await openTab(harness, "settings");
  const section = providerSection(harness);
  const card = section.querySelector(".provider-card");
  assert.ok(card, "the provider card is missing");
  assert.ok(card.classList.contains("provider-card"));

  const title = card.querySelector("h4");
  assert.equal(title.textContent, "deepseek");
  const meta = card.querySelectorAll("p").map((node) => node.textContent).join("\n");
  assert.match(meta, /host: api\.deepseek\.com/);
  assert.match(meta, /Источник: env \(MODEL_PROVIDERS\)/);
  assert.match(meta, /env-список: deepseek-flash, kimi-k3\.1-pro/);
  assert.match(meta, /2 \/ 20/);

  // Labels are the visible text next to the controls, so they carry the muted color contract.
  const labels = card.querySelectorAll("label");
  assert.deepEqual(labels.map((label) => label.className), ["provider-enabled", "provider-field", "provider-field"]);
  assert.ok(labels[0].textContent.includes("Провайдер включён"));
  assert.ok(labels[1].textContent.includes("Модели (по одной в строке)"));
  assert.ok(labels[2].textContent.includes("Модель по умолчанию"));
  assert.equal(labels[0].querySelector("span").textContent, "Провайдер включён");

  // The select keeps the full model string: no ellipsis is rendered by the DOM and the value is exact.
  const select = card.querySelector("select");
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.value), ["deepseek-flash", "kimi-k3.1-pro"]);
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.textContent), ["deepseek-flash", "kimi-k3.1-pro"]);
  assert.equal(select.value, "kimi-k3.1-pro");
  assert.equal(select.value.includes("…"), false);
  assert.equal(select.value.length, "kimi-k3.1-pro".length);

  const toggle = card.querySelector('input[type="checkbox"]');
  assert.equal(toggle.checked, true);
  assert.equal(toggle.getAttribute("aria-label"), "deepseek: Провайдер включён");
  assert.ok(labels[0].classList.contains("provider-enabled"));

  // CSP-safe: no inline style and no inline handler anywhere inside the card.
  for (const node of [card, ...descendants(card)]) {
    assert.equal(node.getAttribute("style"), null, "the provider card uses an inline style");
    for (const name of node.attributes.keys()) assert.doesNotMatch(name, /^on/);
  }

  // The harness has no CSS engine, so the static contract proves the tokens are actually wired up.
  const fs = require("node:fs");
  const path = require("node:path");
  const css = fs.readFileSync(path.join(__dirname, "../static/style.css"), "utf8");
  const rules = styleRules(css);
  const cardCSS = styleBody(rules, ".provider-card");
  assert.match(cardCSS, /background:\s*var\(--card\)/);
  assert.match(cardCSS, /color:\s*var\(--text\)/);
  assert.match(cardCSS, /border:\s*1px solid var\(--border\)/);
  assert.match(cardCSS, /min-width:\s*0/);
  assert.match(cardCSS, /max-width:\s*100%/);
  assert.match(css, /@media \(max-width: 420px\) \{\s*\.provider-card \.settings-row > button \{ flex: 1 1 100%; \}/);
  assert.match(styleBody(rules, ".provider-card .meta"), /color:\s*var\(--muted\)/);
  assert.match(styleBody(rules, ".provider-field"), /color:\s*var\(--muted\)/);
  assert.match(styleBody(rules, ".provider-enabled"), /color:\s*var\(--text\)/);
  assert.match(styleBody(rules, ".provider-field span"), /color:\s*var\(--muted\)/);
  const providerSelect = styleBody(rules, ".provider-field select");
  assert.match(providerSelect, /min-height:\s*46px/);
  assert.match(providerSelect, /height:\s*auto/);
  assert.match(providerSelect, /line-height:\s*1\.4/);
  assert.match(providerSelect, /color:\s*var\(--text\)/);
  assert.match(styleBody(rules, ".provider-field textarea"), /min-height:\s*88px/);
  assert.match(styleBody(rules, "select"), /height:\s*auto/);
  assert.match(styleBody(rules, "select"), /line-height:\s*1\.4/);
  assert.match(styleBody(rules, "input:focus-visible"), /outline:\s*3px solid var\(--focus-ring\)/);
});

test("theme switch applies immediately, persists in localStorage and survives a language change", async () => {
  const memory = new Map();
  const localStorage = {
    getItem(key) { return memory.has(key) ? memory.get(key) : null; },
    setItem(key, value) { memory.set(key, String(value)); },
  };
  const harness = await createHarness({ globals: { localStorage } });
  assert.equal(harness.document.documentElement.getAttribute("data-theme"), "graphite");

  await openTab(harness, "settings");
  const select = harness.element("theme");
  assert.ok(select.classList.contains("theme-select"));
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.value), ["graphite", "sand", "ocean", "forest", "lavender"]);
  assert.deepEqual(select.querySelectorAll("option").map((option) => option.textContent), [
    "Графит (мягкая тёмная)", "Песочная (тёплая светлая)", "Океан (серо-синяя)",
    "Лес (приглушённая зелёная)", "Лаванда (пыльная светлая)",
  ]);
  assert.equal(select.value, "graphite");
  assert.equal(memory.get("dsh-theme"), undefined);

  harness.resetRequests();
  select.value = "ocean";
  select.dispatchEvent({ type: "change" });
  assert.equal(harness.document.documentElement.getAttribute("data-theme"), "ocean");
  assert.equal(memory.get("dsh-theme"), "ocean");
  assert.equal(select.value, "ocean");
  assert.equal(harness.requests.length, 0, "a theme switch must not call the API");

  select.value = "lavender";
  select.dispatchEvent({ type: "change" });
  assert.equal(harness.document.documentElement.getAttribute("data-theme"), "lavender");
  assert.equal(memory.get("dsh-theme"), "lavender");

  const language = harness.element("language");
  language.value = "en";
  language.dispatchEvent({ type: "change" });
  assert.equal(harness.element("theme").value, "lavender");
  assert.equal(harness.document.documentElement.getAttribute("data-theme"), "lavender");
  assert.equal(memory.get("dsh-theme"), "lavender");
});

test("unknown stored themes fall back to graphite and prefers-color-scheme only seeds the default", async () => {
  const unknown = await createHarness({
    globals: {
      localStorage: { getItem: () => "neon", setItem() {} },
      matchMedia: () => ({ matches: true }),
    },
  });
  assert.equal(unknown.document.documentElement.getAttribute("data-theme"), "graphite");

  const light = await createHarness({
    globals: { matchMedia: (query) => ({ matches: /prefers-color-scheme: light/.test(query), media: query }) },
  });
  assert.equal(light.document.documentElement.getAttribute("data-theme"), "sand");

  const dark = await createHarness({ globals: { matchMedia: () => ({ matches: false }) } });
  assert.equal(dark.document.documentElement.getAttribute("data-theme"), "graphite");

  await openTab(dark, "settings");
  const select = dark.element("theme");
  select.value = "neon";
  select.dispatchEvent({ type: "change" });
  assert.equal(dark.document.documentElement.getAttribute("data-theme"), "graphite");
  assert.equal(select.value, "graphite", "the selector falls back to the applied theme");
});

test("all five muted theme token sets are complete and free of pure black or white", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const css = fs.readFileSync(path.join(__dirname, "../static/style.css"), "utf8");
  const required = [
    "--bg", "--surface", "--card", "--field-bg", "--text", "--muted", "--border", "--border-strong",
    "--primary", "--primary-contrast", "--secondary", "--accent", "--accent-strong", "--accent-contrast",
    "--link", "--success", "--success-strong", "--success-contrast", "--warning", "--warning-strong",
    "--warning-contrast", "--danger", "--danger-strong", "--danger-contrast", "--hover-bg", "--active-bg",
    "--focus-ring", "--disabled-bg", "--disabled-text", "--bubble-assistant-bg", "--bubble-user-bg",
    "--bubble-user-border", "--badge-bg", "--badge-text", "--badge-border", "--code-bg", "--code-text",
    "--code-border", "--quote-border", "--chart-grid", "--chart-axis", "--chart-in", "--chart-out",
    "--chart-cost", "--chart-track", "--chart-tool", "--table-head-bg", "--table-row-border", "--modal-bg",
    "--modal-overlay", "--toast-bg", "--toast-border", "--bg-glow-1", "--bg-glow-2", "--input-border",
    "--input-hover-border", "--input-focus-border", "--selection-bg", "--shadow",
  ];
  const backgrounds = new Set();
  for (const theme of ["graphite", "sand", "ocean", "forest", "lavender"]) {
    const match = new RegExp(`html\\[data-theme="${theme}"\\] \\{([^}]*)\\}`).exec(css);
    assert.ok(match, `${theme} theme block is missing`);
    for (const variable of required) assert.ok(match[1].includes(`${variable}:`), `${theme} misses ${variable}`);
    assert.match(match[1], /color-scheme:\s*(?:dark|light)/);
    assert.doesNotMatch(match[1], /#[0]{3}(?![0-9a-f])|#[f]{3}(?![0-9a-f])/i, `${theme} uses pure black or white`);
    assert.doesNotMatch(match[1], /\b(?:white|black)\b/, `${theme} uses a white/black keyword`);
    backgrounds.add((/--bg:\s*(#[0-9a-f]{6})/i.exec(match[1]) || [])[1]);
  }
  assert.equal(backgrounds.size, 5, "the five themes must have five page backgrounds");
  assert.match(css, /:root, html\[data-theme="graphite"\] \{/);
  assert.match(css, /@media \(prefers-color-scheme: light\) \{\s*:root:not\(\[data-theme\]\) \{/);
  assert.match(css, /:root:not\(\[data-theme\]\) \{[^}]*--bg: #f3ece1;/);
  assert.match(css, /\.usage-chart-bar \{ fill: var\(--chart-in\); \}/);
  assert.match(css, /\.usage-chart-line-out \{ fill: none; stroke: var\(--chart-out\);/);
  assert.match(css, /\.usage-chart-cost \{ fill: var\(--chart-cost\); \}/);
  assert.match(css, /\.usage-chart-grid \{ stroke: var\(--chart-grid\);/);
  assert.match(css, /\.provider-card \{[^}]*var\(--card\)/);
  assert.doesNotMatch(css, /:root:not\(\[data-theme\]\) \{[^}]*\b(?:white|black)\b/, "the light default uses a white/black keyword");
});

test("every theme keeps AA contrast for body text, labels, status and controls", () => {
  const fs = require("node:fs");
  const path = require("node:path");
  const css = fs.readFileSync(path.join(__dirname, "../static/style.css"), "utf8");
  const channel = (hex) => {
    const value = parseInt(hex, 16) / 255;
    return value <= 0.03928 ? value / 12.92 : ((value + 0.055) / 1.055) ** 2.4;
  };
  const luminance = (hex) => 0.2126 * channel(hex.slice(1, 3)) + 0.7152 * channel(hex.slice(3, 5)) + 0.0722 * channel(hex.slice(5, 7));
  const contrast = (first, second) => {
    const a = luminance(first);
    const b = luminance(second);
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  };
  const pairs = [
    ["--text", "--bg"], ["--text", "--card"], ["--text", "--field-bg"],
    ["--muted", "--bg"], ["--muted", "--card"], ["--muted", "--field-bg"], ["--muted", "--surface"], ["--text", "--surface"],
    ["--accent-contrast", "--accent"], ["--accent-contrast", "--accent-strong"], ["--accent", "--card"],
    ["--link", "--card"], ["--success", "--card"], ["--warning", "--card"], ["--danger", "--card"],
    ["--badge-text", "--card"], ["--code-text", "--code-bg"], ["--chart-axis", "--card"],
    ["--danger-contrast", "--danger-strong"], ["--success-contrast", "--success-strong"],
    ["--warning-contrast", "--warning-strong"], ["--disabled-text", "--disabled-bg"],
    ["--text", "--toast-bg"], ["--text", "--bubble-assistant-bg"], ["--text", "--bubble-user-bg"],
    ["--muted", "--bubble-assistant-bg"],
  ];
  for (const theme of ["graphite", "sand", "ocean", "forest", "lavender"]) {
    const match = new RegExp(`html\\[data-theme="${theme}"\\] \\{([^}]*)\\}`).exec(css);
    assert.ok(match, `${theme} theme block is missing`);
    const tokens = {};
    for (const declaration of match[1].matchAll(/--([a-z0-9-]+):\s*(#[0-9a-fA-F]{6})\b/g)) tokens[`--${declaration[1]}`] = declaration[2];
    for (const [foreground, background] of pairs) {
      assert.ok(tokens[foreground] && tokens[background], `${theme} misses ${foreground} or ${background}`);
      const ratio = contrast(tokens[foreground], tokens[background]);
      assert.ok(ratio >= 4.5, `${theme} ${foreground} on ${background} = ${ratio.toFixed(2)}:1, below WCAG AA 4.5:1`);
    }
    // A field boundary is non-text UI information: WCAG 1.4.11 asks for 3:1 against the field.
    const edge = contrast(tokens["--input-border"], tokens["--field-bg"]);
    assert.ok(edge >= 3, `${theme} --input-border on --field-bg = ${edge.toFixed(2)}:1, below 3:1`);
  }
});
