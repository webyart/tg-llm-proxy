"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createHarness, findButton, jsonResponse, openTab } = require("./harness.js");

// recorderEnvironment builds the smallest MediaRecorder/getUserMedia mock the Mini App calls:
// isTypeSupported, start, stop (emits dataavailable + stop), tracks with stop().
function recorderEnvironment(options = {}) {
  const created = { gumCalls: 0, streams: [], recorders: [] };
  const tracks = [];
  const chunk = options.chunk === undefined ? "audio-payload".repeat(128) : options.chunk;
  const gum = options.gum || (async () => {
    created.gumCalls += 1;
    const track = { kind: "audio", stopped: false, stop() { this.stopped = true; } };
    tracks.push(track);
    const stream = { tracks: [track], getTracks() { return this.tracks; } };
    created.streams.push(stream);
    return stream;
  });
  class MockMediaRecorder {
    static isTypeSupported(mime) {
      return (options.supported || ["audio/webm;codecs=opus"]).includes(mime);
    }

    constructor(stream, config) {
      this.stream = stream;
      this.mimeType = (config && config.mimeType) || "audio/webm";
      this.state = "inactive";
      this.listeners = new Map();
      created.recorders.push(this);
    }

    addEventListener(type, handler) {
      if (!this.listeners.has(type)) this.listeners.set(type, []);
      this.listeners.get(type).push(handler);
    }

    emit(type, event = {}) {
      for (const handler of this.listeners.get(type) || []) handler.call(this, event);
    }

    start() {
      this.state = "recording";
    }

    stop() {
      if (this.state === "inactive") return;
      this.state = "inactive";
      this.emit("dataavailable", { data: new Blob([chunk], { type: this.mimeType }) });
      this.emit("stop");
    }
  }
  return {
    created,
    tracks,
    globals: { navigator: { mediaDevices: { getUserMedia: gum } }, MediaRecorder: MockMediaRecorder },
  };
}

async function openComposer(harness) {
  await openTab(harness, "chat");
  return harness.element("content");
}

test("the mic records, uploads and inserts the trimmed transcript without sending", async () => {
  const env = recorderEnvironment();
  const harness = await createHarness({ globals: env.globals });
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/transcribe") return jsonResponse({ text: "  привет из записи  " });
    return fallback();
  });
  const content = await openComposer(harness);

  const mic = content.querySelector(".chat-mic");
  assert.ok(mic);
  assert.equal(mic.getAttribute("aria-label"), "Записать голосовое");
  assert.equal(mic.getAttribute("aria-pressed"), "false");
  mic.click();
  await harness.flush();

  assert.equal(env.created.gumCalls, 1);
  assert.equal(env.created.recorders.length, 1);
  const recorder = env.created.recorders[0];
  assert.equal(recorder.state, "recording");
  assert.equal(recorder.mimeType, "audio/webm;codecs=opus");
  assert.equal(content.querySelector(".chat-mic").getAttribute("aria-pressed"), "true");
  assert.ok(content.querySelector(".chat-voice-time"));
  assert.ok(findButton(content, "Остановить запись"));
  assert.ok(findButton(content, "Отменить запись"));

  findButton(content, "Остановить запись").click();
  await harness.flush();

  const uploaded = harness.requests.filter((request) => request.path === "/api/transcribe");
  assert.equal(uploaded.length, 1);
  assert.equal(uploaded[0].options.method, "POST");
  assert.equal(uploaded[0].options.headers.get("Authorization"), "Bearer test-session");
  const file = uploaded[0].options.body.get("file");
  assert.ok(file);
  assert.equal(file.name, "voice.webm");
  assert.equal(file.type, "audio/webm;codecs=opus");

  const input = content.querySelector(".chat-input");
  assert.equal(input.value, "привет из записи");
  assert.equal(harness.document.activeElement, input);
  assert.equal(content.querySelector(".chat-mic").getAttribute("aria-pressed"), "false");
  assert.equal(env.tracks[0].stopped, true);
  assert.equal(harness.element("notice").textContent, "Расшифровка вставлена в поле ввода.");
  assert.equal(harness.element("error").hidden, true);
  // The transcript is placed in the composer, never auto-sent.
  assert.equal(harness.requests.filter((request) => request.path === "/api/messages").length, 0);
});

test("cancel stops the tracks and never uploads", async () => {
  const env = recorderEnvironment();
  const harness = await createHarness({ globals: env.globals });
  const content = await openComposer(harness);
  content.querySelector(".chat-mic").click();
  await harness.flush();

  findButton(content, "Отменить запись").click();
  await harness.flush();

  assert.equal(env.created.recorders.length, 1);
  assert.equal(env.created.recorders[0].state, "inactive");
  assert.equal(env.tracks[0].stopped, true);
  assert.equal(harness.requests.filter((request) => request.path === "/api/transcribe").length, 0);
  assert.equal(content.querySelector(".chat-mic").getAttribute("aria-pressed"), "false");
  assert.equal(harness.element("error").hidden, true);
});

test("a denied microphone shows the localized permission message", async () => {
  const env = recorderEnvironment({ gum: async () => { throw new Error("NotAllowedError"); } });
  const harness = await createHarness({ globals: env.globals });
  const content = await openComposer(harness);

  content.querySelector(".chat-mic").click();
  await harness.flush();

  assert.equal(env.created.recorders.length, 0);
  assert.equal(harness.element("error").textContent, "Нет доступа к микрофону. Разрешите его в настройках браузера или отправьте голосовое в Telegram.");
  assert.equal(harness.element("error").hidden, false);
});

test("an unsupported browser degrades with a clear Telegram message", async () => {
  const harness = await createHarness();
  const content = await openComposer(harness);
  const mic = content.querySelector(".chat-mic");
  assert.ok(mic, "the mic stays visible so the user learns why it cannot record");

  mic.click();
  await harness.flush();

  assert.equal(harness.element("error").textContent, "Браузер не поддерживает запись звука. Отправьте голосовое сообщение в Telegram.");
  assert.equal(harness.requests.filter((request) => request.path === "/api/transcribe").length, 0);
});

test("a too-short recording is refused locally before the upload", async () => {
  const env = recorderEnvironment({ chunk: "x" });
  const harness = await createHarness({ globals: env.globals });
  const content = await openComposer(harness);
  content.querySelector(".chat-mic").click();
  await harness.flush();
  findButton(content, "Остановить запись").click();
  await harness.flush();

  assert.equal(harness.element("error").textContent, "Запись слишком короткая. Говорите дольше и остановите запись.");
  assert.equal(harness.requests.filter((request) => request.path === "/api/transcribe").length, 0);
  assert.equal(content.querySelector(".chat-mic").getAttribute("aria-pressed"), "false");
});

test("a network failure while uploading is localized", async () => {
  const env = recorderEnvironment();
  const harness = await createHarness({ globals: env.globals });
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/transcribe") return Promise.reject(new Error("offline"));
    return fallback();
  });
  const content = await openComposer(harness);
  content.querySelector(".chat-mic").click();
  await harness.flush();
  findButton(content, "Остановить запись").click();
  await harness.flush();

  assert.equal(harness.element("error").textContent, "Не удалось отправить запись. Проверьте соединение и повторите.");
  assert.equal(content.querySelector(".chat-mic").getAttribute("aria-pressed"), "false");
});
