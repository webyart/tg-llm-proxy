"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createHarness, findButton, jsonResponse, openTab } = require("./harness.js");

function documentFile(name, content, type) {
  return new File([content], name, { type });
}

async function openDocuments() {
  const harness = await createHarness();
  await openTab(harness, "documents");
  harness.resetRequests();
  return harness;
}

function fileInput(harness) {
  return harness.element("content").querySelector('input[type="file"]');
}

function pick(harness, file) {
  const input = fileInput(harness);
  input.files = [file];
  input.dispatchEvent({ type: "change" });
  return input;
}

test("Documents tab renders a 44px-friendly upload picker with the server extension filter", async () => {
  const harness = await openDocuments();
  const content = harness.element("content");
  assert.match(content.textContent, /Загрузить документ/);
  assert.match(content.textContent, /64 КиБ/);
  assert.match(content.textContent, /20 МиБ/);
  const input = fileInput(harness);
  assert.ok(input);
  assert.ok(input.accept.includes(".txt"));
  assert.ok(input.accept.includes(".md"));
  assert.ok(input.accept.includes(".pdf"));
  assert.ok(input.accept.includes(".docx"));
  assert.ok(input.accept.includes("text/*"));
  assert.equal(input.getAttribute("aria-label"), "Файл для загрузки");
  assert.equal(findButton(content, "Загрузить").type, "button");

  const language = harness.element("language");
  language.value = "en";
  language.dispatchEvent({ type: "change" });
  assert.match(harness.element("content").textContent, /Upload a document/);
  assert.match(harness.element("content").textContent, /20 MiB/);
  assert.ok(findButton(harness.element("content"), "Upload"));
});

test("client pre-check rejects a binary extension, an empty file and an oversized text before fetch", async () => {
  const harness = await openDocuments();
  const content = harness.element("content");
  pick(harness, documentFile("photo.png", "not really an image", "image/png"));
  findButton(content, "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /Поддерживаются/);

  pick(harness, documentFile("old.doc", "legacy binary", "application/msword"));
  findButton(content, "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /\.doc/);

  pick(harness, documentFile("empty.txt", "", "text/plain"));
  findButton(harness.element("content"), "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /Файл пустой/);

  pick(harness, documentFile("huge.txt", "a".repeat(64 * 1024 + 1), "text/plain"));
  findButton(harness.element("content"), "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /больше 64 КиБ/);
});

test("client size limits are per type: text 64 KiB, PDF/DOCX 20 MiB", async () => {
  const harness = await openDocuments();
  const content = harness.element("content");
  function sized(name, type, size) {
    const file = documentFile(name, "content", type);
    Object.defineProperty(file, "size", { value: size });
    return file;
  }

  pick(harness, sized("manual.pdf", "application/pdf", 20 * 1024 * 1024 + 1));
  findButton(content, "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /20 МиБ/);

  pick(harness, sized("notes.txt", "text/plain", 64 * 1024 + 1));
  findButton(content, "Загрузить").click();
  assert.equal(harness.requests.length, 0);
  assert.match(harness.element("error").textContent, /64 КиБ/);

  harness.setHandler((request, fallback) => {
    if (request.path === "/api/documents" && request.options.method === "POST") {
      return jsonResponse({ id: 7, name: "manual.pdf", chunks: 1, embedded: false }, 201);
    }
    return fallback();
  });
  // Exactly the Bot API getFile ceiling is allowed; the extension decides the limit even with an empty MIME.
  pick(harness, sized("manual.pdf", "", 20 * 1024 * 1024));
  findButton(content, "Загрузить").click();
  await harness.flush();
  const uploads = harness.requests.filter((request) => request.path === "/api/documents" && request.options.method === "POST");
  assert.equal(uploads.length, 1);
  assert.equal(uploads[0].options.body.get("file").name, "manual.pdf");
  assert.equal(harness.element("notice").textContent, "Документ загружен.");
});

test("upload posts multipart FormData, shows the loading state and refreshes the list", async () => {
  const harness = await openDocuments();
  const content = harness.element("content");
  let release;
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/documents" && request.options.method === "POST") {
      return new Promise((resolve) => {
        release = () => resolve(jsonResponse({ id: 5, name: "notes.txt", chunks: 1, embedded: false }, 201));
      });
    }
    return fallback();
  });
  pick(harness, documentFile("notes.txt", "phone notes", "text/plain"));
  assert.match(content.textContent, /notes\.txt/);
  const button = findButton(content, "Загрузить");
  button.click();

  assert.equal(button.textContent, "Загрузка…");
  assert.equal(button.disabled, true);
  assert.equal(harness.element("loading").hidden, false);
  assert.equal(harness.element("content").getAttribute("aria-busy"), "true");

  release();
  await harness.flush();

  const uploads = harness.requests.filter((request) => request.path === "/api/documents" && request.options.method === "POST");
  assert.equal(uploads.length, 1);
  assert.equal(uploads[0].options.body instanceof FormData, true);
  const part = uploads[0].options.body.get("file");
  assert.equal(part.name, "notes.txt");
  assert.equal(part.type, "text/plain");
  assert.equal(await part.text(), "phone notes");
  assert.equal(uploads[0].options.headers.get("Authorization"), "Bearer test-session");
  assert.ok(!uploads[0].options.headers.get("Content-Type"), "the browser must set the multipart boundary");
  assert.ok(harness.requests.some((request) => request.path === "/api/documents?limit=10"));
  assert.equal(harness.element("loading").hidden, true);
  assert.equal(harness.element("notice").textContent, "Документ загружен.");
  assert.equal(harness.element("notice").hidden, false);
});

test("a server-side limit error is shown and the upload can be retried", async () => {
  const harness = await openDocuments();
  harness.setHandler((request, fallback) => {
    if (request.path === "/api/documents" && request.options.method === "POST") {
      return jsonResponse({ error: { code: "document_limit" } }, 409);
    }
    return fallback();
  });
  pick(harness, documentFile("notes.txt", "phone notes", "text/plain"));
  findButton(harness.element("content"), "Загрузить").click();
  await harness.flush();

  assert.match(harness.element("error").textContent, /Достигнут лимит документов/);
  assert.equal(harness.element("notice").hidden, true);
  const content = harness.element("content");
  assert.equal(findButton(content, "Загрузить").textContent, "Загрузить");
  assert.equal(findButton(content, "Загрузить").disabled, false);
  // The File object survived the re-render, so a retry needs no second picker interaction.
  findButton(content, "Загрузить").click();
  await harness.flush();
  assert.equal(harness.requests.filter((request) => request.path === "/api/documents" && request.options.method === "POST").length, 2);
});
