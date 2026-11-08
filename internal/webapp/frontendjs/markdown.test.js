"use strict";

const test = require("node:test");
const assert = require("node:assert/strict");
const { createHarness, findArticle, jsonResponse, openTab } = require("./harness.js");

function historyWith(content) {
  return {
    items: [{ id: "assistant-1", role: "assistant", content, created_at: "2026-01-01", feedback: 0 }],
    next_before_id: null,
  };
}

async function renderAssistant(content) {
  const harness = await createHarness();
  harness.setHandler((request, fallback) => {
    if (request.path.startsWith("/api/history?")) return jsonResponse(historyWith(content));
    return fallback();
  });
  await openTab(harness, "history");
  const contentNode = harness.element("content");
  return { harness, contentNode, article: findArticle(contentNode, "") };
}

test("assistant history renders the supported markdown subset as safe DOM nodes", async () => {
  const markdown = [
    "## Заголовок",
    "",
    "**жирный** и *курсив* и ~~зачёркнутый~~ и `code`.",
    "",
    "- первый",
    "- второй",
    "",
    "1. один",
    "2. два",
    "",
    "> цитата",
    "",
    "[ссылка](https://example.test/a?x=1&y=2) и https://example.test/bare.",
    "",
    "```js",
    "const answer = 42;",
    "```",
  ].join("\n");
  const { contentNode, article } = await renderAssistant(markdown);

  assert.equal(contentNode.querySelectorAll("article").length, 1);
  assert.equal(article.querySelectorAll("h2").length, 1);
  assert.equal(article.querySelectorAll("strong").length, 1);
  assert.equal(article.querySelectorAll("em").length, 1);
  assert.equal(article.querySelectorAll("del").length, 1);
  assert.equal(article.querySelectorAll("ul").length, 1);
  assert.equal(article.querySelectorAll("ol").length, 1);
  assert.equal(article.querySelectorAll("blockquote").length, 1);
  assert.equal(article.querySelectorAll("pre").length, 1);
  assert.equal(article.querySelectorAll(".md-code-lang").length, 1);
  assert.match(article.textContent, /const answer = 42;/);
  assert.doesNotMatch(article.textContent, /```/);

  const anchors = article.querySelectorAll("a");
  assert.equal(anchors.length, 2);
  assert.equal(anchors[0].href, "https://example.test/a?x=1&y=2");
  assert.equal(anchors[0].rel, "noopener noreferrer");
  assert.equal(anchors[0].target, "_blank");
  assert.equal(anchors[1].href, "https://example.test/bare");
  assert.doesNotMatch(anchors[1].href, /\.$/);
});

test("markdown never turns model or user text into markup", async () => {
  const payload = '<img src=x onerror="alert(1)"> <script>alert(2)</script> [x](javascript:alert(3))';
  const { article } = await renderAssistant(payload);

  assert.equal(article.querySelectorAll("img").length, 0);
  assert.equal(article.querySelectorAll("script").length, 0);
  assert.equal(article.querySelectorAll("a").length, 0);
  assert.match(article.textContent, /<img src=x onerror="alert\(1\)">/);
  assert.match(article.textContent, /<script>alert\(2\)<\/script>/);
  assert.match(article.textContent, /javascript:alert\(3\)/);
});

test("partial markdown from a stream renders gracefully without broken tags", async () => {
  const partial = "**жирный не закрыт\n\n```js\nconst x = 1;";
  const { article } = await renderAssistant(partial);

  assert.equal(article.querySelectorAll("strong").length, 0);
  assert.match(article.textContent, /\*\*жирный не закрыт/);
  assert.equal(article.querySelectorAll("pre").length, 1);
  assert.equal(article.querySelectorAll("code").length, 1);
  assert.match(article.textContent, /const x = 1;/);
  assert.equal(article.querySelectorAll("script").length, 0);
});

test("single newlines stay line breaks inside one paragraph", async () => {
  const { article } = await renderAssistant("первая строка\nвторая строка");
  const paragraphs = article.querySelectorAll("p");
  assert.equal(paragraphs.length, 1);
  assert.equal(paragraphs[0].querySelectorAll("br").length, 1);
  assert.match(paragraphs[0].textContent, /первая строка\s*вторая строка/);
});
