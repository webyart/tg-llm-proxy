"use strict";

const fs = require("node:fs");
const path = require("node:path");
const vm = require("node:vm");

function dataName(attribute) {
  return attribute.slice(5).replace(/-([a-z])/g, (_, letter) => letter.toUpperCase());
}

function matches(element, selector) {
  if (selector.startsWith("#")) return element.id === selector.slice(1);
  if (selector.startsWith(".")) return element.classList.contains(selector.slice(1));
  const match = /^([a-zA-Z][a-zA-Z0-9-]*)?(?:\[([a-zA-Z0-9-]+)(?:="([^"]*)")?\])?$/.exec(selector);
  if (!match) throw new Error(`Unsupported selector: ${selector}`);
  const [, tag, attribute, expected] = match;
  if (tag && element.tagName !== tag.toUpperCase()) return false;
  if (!attribute) return true;
  let value;
  if (attribute.startsWith("data-")) value = element.dataset[dataName(attribute)];
  else if (attribute === "class") value = element.className;
  else if (attribute === "id") value = element.id;
  else if (element.attributes.has(attribute)) value = element.attributes.get(attribute);
  else value = element[attribute];
  if (value === undefined || value === null || value === false) return false;
  return expected === undefined || String(value) === expected;
}

class FakeElement {
  constructor(document, tagName) {
    this.ownerDocument = document;
    this.tagName = tagName.toUpperCase();
    this.parentNode = null;
    this.childNodes = [];
    this.attributes = new Map();
    this.dataset = {};
    this.listeners = new Map();
    this.className = "";
    this.id = "";
    this.hidden = false;
    this.disabled = false;
    this.value = "";
    this._text = "";
    this.classList = {
      contains: (name) => this.className.split(/\s+/).includes(name),
      toggle: (name, force) => {
        const names = new Set(this.className.split(/\s+/).filter(Boolean));
        if (force) names.add(name);
        else names.delete(name);
        this.className = Array.from(names).join(" ");
      },
    };
  }

  get children() {
    return this.childNodes;
  }

  get textContent() {
    return this._text + this.childNodes.map((child) => child.textContent).join("");
  }

  set textContent(value) {
    this._text = String(value);
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes = [];
  }

  appendChild(child) {
    if (child.parentNode) child.remove();
    child.parentNode = this;
    this.childNodes.push(child);
    return child;
  }

  replaceChildren(...children) {
    for (const child of this.childNodes) child.parentNode = null;
    this.childNodes = [];
    this._text = "";
    for (const child of children) this.appendChild(child);
  }

  remove() {
    if (!this.parentNode) return;
    const index = this.parentNode.childNodes.indexOf(this);
    if (index >= 0) this.parentNode.childNodes.splice(index, 1);
    this.parentNode = null;
  }

  setAttribute(name, value) {
    const text = String(value);
    this.attributes.set(name, text);
    if (name.startsWith("data-")) this.dataset[dataName(name)] = text;
    if (name === "id") this.id = text;
  }

  getAttribute(name) {
    if (this.attributes.has(name)) return this.attributes.get(name);
    if (name.startsWith("data-")) return this.dataset[dataName(name)];
    return null;
  }

  addEventListener(type, handler) {
    if (!this.listeners.has(type)) this.listeners.set(type, []);
    this.listeners.get(type).push(handler);
  }

  dispatchEvent(event) {
    if (!event.target) event.target = this;
    if (!event.preventDefault) event.preventDefault = () => { event.defaultPrevented = true; };
    let current = this;
    while (current) {
      event.currentTarget = current;
      for (const handler of current.listeners.get(event.type) || []) handler.call(current, event);
      if (!event.bubbles) break;
      current = current.parentNode;
    }
    return !event.defaultPrevented;
  }

  click() {
    if (this.disabled) return;
    if (this.tagName === "A") this.ownerDocument.downloads.push({ element: this, href: this.href, download: this.download });
    const allowed = this.dispatchEvent({ type: "click", bubbles: true });
    const form = this.type === "submit" ? this.closest("form") : null;
    if (allowed && form) form.dispatchEvent({ type: "submit", bubbles: false, target: this });
  }

  focus() {
    this.ownerDocument.activeElement = this;
  }

  closest(selector) {
    let current = this;
    while (current) {
      if (matches(current, selector)) return current;
      current = current.parentNode;
    }
    return null;
  }

  querySelectorAll(selector) {
    const selectors = selector.split(",").map((part) => part.trim());
    const found = [];
    const visit = (node) => {
      for (const child of node.childNodes) {
        if (selectors.some((part) => matches(child, part))) found.push(child);
        visit(child);
      }
    };
    visit(this);
    return found;
  }

  querySelector(selector) {
    return this.querySelectorAll(selector)[0] || null;
  }
}

class FakeDocument {
  constructor() {
    this.downloads = [];
    this.documentElement = new FakeElement(this, "html");
    this.body = new FakeElement(this, "body");
    this.documentElement.appendChild(this.body);
    this.title = "";
  }

  createElement(tagName) {
    return new FakeElement(this, tagName);
  }

  getElementById(id) {
    return this.documentElement.querySelector(`#${id}`);
  }

  querySelectorAll(selector) {
    return this.documentElement.querySelectorAll(selector);
  }

  querySelector(selector) {
    return this.documentElement.querySelector(selector);
  }
}

function staticElement(document, tag, options = {}) {
  const element = document.createElement(tag);
  if (options.id) element.setAttribute("id", options.id);
  if (options.text !== undefined) element.textContent = options.text;
  if (options.hidden) element.hidden = true;
  if (options.i18n) element.setAttribute("data-i18n", options.i18n);
  if (options.i18nAria) element.setAttribute("data-i18n-aria", options.i18nAria);
  if (options.tab) element.setAttribute("data-tab", options.tab);
  return element;
}

function initialDocument() {
  const document = new FakeDocument();
  const body = document.body;
  const header = staticElement(document, "header");
  header.appendChild(staticElement(document, "h1", { i18n: "headerTitle" }));
  header.appendChild(staticElement(document, "p", { i18n: "headerSubtitle" }));
  const language = staticElement(document, "select", { id: "language", i18nAria: "languageLabel" });
  for (const value of ["ru", "en"]) {
    const option = staticElement(document, "option", { text: value.toUpperCase() });
    option.value = value;
    language.appendChild(option);
  }
  header.appendChild(language);
  header.appendChild(staticElement(document, "button", { id: "logout", i18n: "closeSession", hidden: true }));
  body.appendChild(header);
  body.appendChild(staticElement(document, "div", { id: "error", hidden: true }));
  body.appendChild(staticElement(document, "div", { id: "notice", hidden: true }));
  const launch = staticElement(document, "section", { id: "launch" });
  launch.appendChild(staticElement(document, "h2", { i18n: "launchTitle" }));
  launch.appendChild(staticElement(document, "p", { i18n: "launchText" }));
  body.appendChild(launch);
  const app = staticElement(document, "div", { id: "app", hidden: true });
  const tabs = staticElement(document, "nav", { id: "tabs", i18nAria: "navLabel" });
  for (const tab of ["status", "history", "documents", "links", "tasks", "reminders", "usage", "settings"]) {
    const button = staticElement(document, "button", { id: `tab-${tab}`, tab, i18n: `tab${tab[0].toUpperCase()}${tab.slice(1)}` });
    button.setAttribute("role", "tab");
    button.setAttribute("aria-selected", String(tab === "status"));
    if (tab === "status") button.className = "active";
    tabs.appendChild(button);
  }
  app.appendChild(tabs);
  app.appendChild(staticElement(document, "h2", { id: "section-title" }));
  app.appendChild(staticElement(document, "button", { id: "refresh", i18n: "refresh" }));
  app.appendChild(staticElement(document, "div", { id: "loading", i18n: "loading", hidden: true }));
  app.appendChild(staticElement(document, "div", { id: "content" }));
  app.appendChild(staticElement(document, "button", { id: "more", i18n: "loadOlder", hidden: true }));
  body.appendChild(app);
  return document;
}

function jsonResponse(body, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() { return body; },
  };
}

function blobResponse(blob, status = 200) {
  return {
    ok: status >= 200 && status < 300,
    status,
    async json() { return null; },
    async blob() { return blob; },
  };
}

function settings() {
  return {
    model: { current: "model-a", source: "global", default: "model-a", available: ["model-a", "model-b"], available_truncated: false },
    persona: { current: "default", source: "global", custom: false, presets: [{ name: "assistant", description: "assistant" }] },
    stream: { enabled: true, source: "global" },
    tts: { enabled: true, current: 1 },
  };
}

function defaultResponse(request) {
  const pathname = request.path.split("?")[0];
  if (pathname === "/api/session" && request.options.method === "POST") return jsonResponse({ token: "test-session" });
  if (pathname === "/api/status") return jsonResponse({ messages: 2, documents: 1, links: 0, tasks: 0, active_tasks: 0, pending: false, recovery_suppressed: false });
  if (pathname === "/api/documents") return jsonResponse({ items: [{ id: "doc-1", name: "handbook", size_bytes: 12, chunks: 2, created_at: "2026-01-01" }] });
  if (pathname === "/api/documents/search") return jsonResponse({ items: [], corpus: { truncated: false } });
  if (pathname === "/api/history") return jsonResponse({ items: [{ id: "msg-1", role: "user", content: "hello", created_at: "2026-01-01" }] });
  if (pathname === "/api/history/search") return jsonResponse({ items: [], truncated: false });
  if (pathname === "/api/history/export") return blobResponse({ size: 4 });
  if (pathname === "/api/links/search") return jsonResponse({ items: [] });
  if (pathname === "/api/reminders") {
    return jsonResponse({
      active: [{ id: "active-1", text: "active reminder", cadence: "daily", daily_time: "09:00", next_run: "2026-01-02", attempts: 0 }],
      failed: [{ id: "failed-1", text: "failed reminder", cadence: "once", next_run: "2026-01-01", attempts: 2 }],
    });
  }
  if (pathname === "/api/settings") return jsonResponse(settings());
  if (pathname === "/api/usage") {
    return jsonResponse({ date: "2026-01-01", models: [], tools: { read_url: 0, web_search: 0, calculator: 0, rag: 0 }, limits: { model: { used: 0, limit: 10, remaining: 10 }, network_tools: { used: 0, limit: 10, remaining: 10 } } });
  }
  if (["/api/links", "/api/tasks"].includes(pathname)) return jsonResponse({ items: [] });
  if (request.options.method && request.options.method !== "GET") return jsonResponse({});
  return jsonResponse({ error: { code: "not_found" } }, 404);
}

async function flush() {
  for (let index = 0; index < 6; index += 1) await new Promise((resolve) => setImmediate(resolve));
}

async function createHarness() {
  const document = initialDocument();
  const requests = [];
  const createdObjectURLs = [];
  const revokedObjectURLs = [];
  const telegramCalls = [];
  let handler = (request, fallback) => fallback();
  const sandbox = {
    console,
    setImmediate,
    clearImmediate,
    Headers,
    TextEncoder,
    URLSearchParams,
    localStorage: {
      getItem() { return null; },
      setItem() {},
    },
  };
  sandbox.window = sandbox;
  sandbox.document = document;
  sandbox.location = { hash: "#ticket=ticket-1", pathname: "/miniapp/", search: "?source=test" };
  sandbox.history = { replaceState() {} };
  sandbox.Telegram = {
    WebApp: {
      initData: "signed-init-data",
      ready() { telegramCalls.push("ready"); },
      expand() { telegramCalls.push("expand"); },
    },
  };
  class FakeURL extends URL {
    static createObjectURL(blob) {
      const value = `blob:test-${createdObjectURLs.length + 1}`;
      createdObjectURLs.push({ value, blob });
      return value;
    }

    static revokeObjectURL(value) {
      revokedObjectURLs.push(value);
    }
  }
  sandbox.URL = FakeURL;
  sandbox.fetch = async (requestPath, options = {}) => {
    const request = { path: requestPath, options };
    requests.push(request);
    const fallback = () => defaultResponse(request);
    return handler(request, fallback);
  };
  vm.createContext(sandbox);
  const script = fs.readFileSync(path.join(__dirname, "../static/app.js"), "utf8");
  vm.runInContext(script, sandbox, { filename: "internal/webapp/static/app.js" });
  await flush();
  return {
    document,
    requests,
    createdObjectURLs,
    revokedObjectURLs,
    telegramCalls,
    flush,
    resetRequests() { requests.length = 0; },
    setHandler(nextHandler) { handler = nextHandler; },
    element(id) {
      const result = document.getElementById(id);
      if (!result) throw new Error(`Missing element #${id}`);
      return result;
    },
  };
}

function findButton(root, label) {
  const buttons = root.querySelectorAll("button").filter((button) => button.textContent === label);
  if (buttons.length !== 1) throw new Error(`Expected one ${JSON.stringify(label)} button, found ${buttons.length}`);
  return buttons[0];
}

function findArticle(root, text) {
  const articles = root.querySelectorAll("article").filter((article) => article.textContent.includes(text));
  if (articles.length !== 1) throw new Error(`Expected one article containing ${JSON.stringify(text)}, found ${articles.length}`);
  return articles[0];
}

async function openTab(harness, tab) {
  const button = harness.element("tabs").querySelectorAll("button[data-tab]").find((candidate) => candidate.dataset.tab === tab);
  if (!button) throw new Error(`Missing tab ${tab}`);
  button.click();
  await harness.flush();
}

module.exports = {
  createHarness,
  defaultResponse,
  findArticle,
  findButton,
  flush,
  jsonResponse,
  blobResponse,
  openTab,
};
