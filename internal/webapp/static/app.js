(() => {
  "use strict";

  const launch = new URLSearchParams(window.location.hash.slice(1));
  const ticket = launch.get("ticket") || "";
  const initData = (window.Telegram && window.Telegram.WebApp && window.Telegram.WebApp.initData) || launch.get("tgWebAppData") || "";
  if (window.Telegram && window.Telegram.WebApp) {
    window.Telegram.WebApp.ready();
    window.Telegram.WebApp.expand();
  }
  window.history.replaceState(null, "", window.location.pathname + window.location.search);

  const ui = {
    app: document.getElementById("app"), launch: document.getElementById("launch"),
    error: document.getElementById("error"), content: document.getElementById("content"),
    title: document.getElementById("section-title"), more: document.getElementById("more"),
    refresh: document.getElementById("refresh"), logout: document.getElementById("logout"),
  };
  const labels = { status: "Status", history: "History", documents: "Documents", links: "Saved links", tasks: "Tasks", settings: "Settings" };
  let session = "";
  let activeTab = "status";
  let nextBefore = null;
  let loading = false;

  function showError(message) {
    ui.error.textContent = message;
    ui.error.hidden = !message;
  }

  function expire(message) {
    session = "";
    ui.app.hidden = true;
    ui.logout.hidden = true;
    ui.launch.hidden = false;
    showError(message || "Session expired. Reopen the Mini App from Telegram.");
  }

  function element(tag, className, text) {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  }

  async function api(path, options = {}) {
    const headers = new Headers(options.headers || {});
    if (session) headers.set("Authorization", `Bearer ${session}`);
    const response = await fetch(path, { ...options, headers, cache: "no-store" });
    let body = null;
    try { body = await response.json(); } catch (_) { /* handled as a generic failure below */ }
    if (!response.ok) {
      if (response.status === 401 && path !== "/api/session") {
        expire("Session expired. Reopen the Mini App from Telegram.");
      }
      throw new Error((body && body.error && body.error.message) || `Request failed (${response.status}).`);
    }
    if (!body) throw new Error("The server returned an invalid response.");
    return body;
  }

  function renderEmpty(text) {
    ui.content.replaceChildren(element("p", "empty", text));
  }

  function itemShell(id, timestamp) {
    const wrapper = element("article", "item");
    wrapper.appendChild(element("div", "meta", `#${id}${timestamp ? ` · ${timestamp}` : ""}`));
    return wrapper;
  }

  function renderStatus(data) {
    ui.content.replaceChildren();
    if (data.last_activity) ui.content.appendChild(element("p", "meta", `Last activity: ${data.last_activity}`));
    const grid = element("div", "stats");
    const values = [
      ["Messages", data.messages], ["Documents", data.documents], ["Saved links", data.links],
      ["Tasks", data.tasks], ["Active tasks", data.active_tasks], ["Awaiting answer", data.pending ? "Yes" : "No"],
    ];
    for (const [label, value] of values) {
      const card = element("div", "stat");
      card.appendChild(element("strong", "", String(value)));
      card.appendChild(element("span", "", label));
      grid.appendChild(card);
    }
    ui.content.appendChild(grid);
  }

  function renderItems(tab, items, append) {
    if (!append) ui.content.replaceChildren();
    if (!items.length && !append) {
      renderEmpty(tab === "history" ? "No messages yet." : "Nothing here yet.");
      return;
    }
    for (const item of items) {
      const wrapper = itemShell(item.id, item.created_at || item.updated_at);
      if (tab === "history") {
        wrapper.appendChild(element("div", "role", item.role === "user" ? "You" : "Assistant"));
        wrapper.appendChild(element("div", "content", item.content));
      } else if (tab === "documents") {
        wrapper.appendChild(element("div", "content", item.name));
        wrapper.appendChild(element("div", "meta", `${item.size_bytes} bytes · ${item.chunks} chunks`));
      } else if (tab === "links") {
        wrapper.appendChild(element("div", "content", item.title));
        if (/^https:\/\//i.test(item.url)) {
          const anchor = element("a", "", item.url);
          anchor.href = item.url;
          anchor.target = "_blank";
          anchor.rel = "noopener noreferrer";
          wrapper.appendChild(anchor);
        } else {
          wrapper.appendChild(element("div", "content", item.url));
        }
        wrapper.appendChild(element("div", "content", item.content));
      } else if (tab === "tasks") {
        wrapper.appendChild(element("div", "content", item.goal));
        wrapper.appendChild(element("div", "meta", `${item.status} · step ${item.next_step} · ${item.attempts} attempts · due ${item.due_at}`));
      }
      ui.content.appendChild(wrapper);
    }
  }

  function settingsButton(label, handler, secondary = false) {
    const button = element("button", secondary ? "secondary" : "", label);
    button.type = "button";
    button.addEventListener("click", handler);
    return button;
  }

  function settingsSection(title, description) {
    const section = element("section", "settings-section");
    section.appendChild(element("h3", "", title));
    section.appendChild(element("p", "meta", description));
    return section;
  }

  async function changeSettings(path, method, payload) {
    if (loading || !session) return;
    loading = true;
    ui.refresh.disabled = true;
    showError("");
    let saved = false;
    try {
      const options = { method };
      if (payload !== undefined) {
        options.headers = { "Content-Type": "application/json" };
        options.body = JSON.stringify(payload);
      }
      await api(path, options);
      saved = true;
    } catch (error) {
      showError(error.message || "The setting could not be saved.");
    } finally {
      loading = false;
      ui.refresh.disabled = false;
    }
    if (saved) {
      await load();
      showError("Settings saved for this chat.");
    }
  }

  function renderSettings(data) {
    ui.content.replaceChildren();
    const source = (value) => value === "chat" ? "this chat override" : "global default";

    const model = settingsSection("Model", `Current: ${data.model.current} · ${source(data.model.source)} · Global default: ${data.model.default}`);
    const modelChoice = element("select");
    modelChoice.setAttribute("aria-label", "Available provider models");
    for (const name of data.model.available) {
      const option = element("option", "", name);
      option.value = name;
      option.selected = name === data.model.current;
      modelChoice.appendChild(option);
    }
    if (!data.model.available.length) {
      const option = element("option", "", "No models reported by the provider");
      option.disabled = true;
      modelChoice.appendChild(option);
    }
    const modelRow = element("div", "settings-row");
    modelRow.appendChild(modelChoice);
    modelRow.appendChild(settingsButton("Use model", () => changeSettings("/api/settings/model", "PUT", { model: modelChoice.value })));
    modelRow.appendChild(settingsButton("Reset to default", () => changeSettings("/api/settings/model", "DELETE"), true));
    model.appendChild(modelRow);
    if (data.model.available_truncated) model.appendChild(element("p", "meta", "Only the first 50 provider models are shown; refresh to check the endpoint again."));
    ui.content.appendChild(model);

    const persona = settingsSection("Persona", `Current: ${data.persona.current} · ${source(data.persona.source)}${data.persona.custom ? " · custom text" : ""}`);
    const presetChoice = element("select");
    presetChoice.setAttribute("aria-label", "Persona presets");
    for (const preset of data.persona.presets) {
      const option = element("option", "", `${preset.name} — ${preset.description}`);
      option.value = preset.name;
      option.selected = preset.name === data.persona.current;
      presetChoice.appendChild(option);
    }
    const presetRow = element("div", "settings-row");
    presetRow.appendChild(presetChoice);
    presetRow.appendChild(settingsButton("Use preset", () => changeSettings("/api/settings/persona", "PUT", { preset: presetChoice.value })));
    presetRow.appendChild(settingsButton("Reset persona", () => changeSettings("/api/settings/persona", "DELETE"), true));
    persona.appendChild(presetRow);
    const custom = element("textarea");
    custom.maxLength = 8000;
    custom.placeholder = "Custom persona text (1–4000 Unicode characters)";
    custom.setAttribute("aria-label", "Custom persona text");
    const counter = element("div", "counter", "0 / 4000");
    custom.addEventListener("input", () => { counter.textContent = `${Array.from(custom.value).length} / 4000`; });
    persona.appendChild(custom);
    persona.appendChild(counter);
    const customRow = element("div", "settings-row");
    customRow.appendChild(settingsButton("Save custom persona", () => changeSettings("/api/settings/persona", "PUT", { custom: custom.value })));
    persona.appendChild(customRow);
    ui.content.appendChild(persona);

    const stream = settingsSection("Stream responses", `Current: ${data.stream.enabled ? "on" : "off"} · ${source(data.stream.source)}`);
    const streamChoice = element("select");
    streamChoice.setAttribute("aria-label", "Stream responses");
    for (const [value, label] of [["true", "On"], ["false", "Off"]]) {
      const option = element("option", "", label);
      option.value = value;
      option.selected = String(data.stream.enabled) === value;
      streamChoice.appendChild(option);
    }
    const streamRow = element("div", "settings-row");
    streamRow.appendChild(streamChoice);
    streamRow.appendChild(settingsButton("Save stream setting", () => changeSettings("/api/settings/stream", "PUT", { enabled: streamChoice.value === "true" })));
    streamRow.appendChild(settingsButton("Reset to global", () => changeSettings("/api/settings/stream", "DELETE"), true));
    stream.appendChild(streamRow);
    ui.content.appendChild(stream);
  }

  async function load(append = false) {
    if (loading || !session) return;
    loading = true;
    showError("");
    ui.refresh.disabled = true;
    ui.more.disabled = true;
    try {
      if (activeTab === "status") {
        renderStatus(await api("/api/status"));
        nextBefore = null;
      } else if (activeTab === "settings") {
        renderSettings(await api("/api/settings"));
        nextBefore = null;
      } else {
        const params = new URLSearchParams({ limit: "10" });
        if (append && nextBefore) params.set("before", String(nextBefore));
        const data = await api(`/api/${activeTab}?${params}`);
        nextBefore = data.next_before_id || null;
        renderItems(activeTab, data.items, append);
      }
      ui.more.hidden = activeTab === "status" || activeTab === "settings" || !nextBefore;
    } catch (error) {
      showError(error.message || "The data could not be loaded.");
    } finally {
      loading = false;
      ui.refresh.disabled = false;
      ui.more.disabled = false;
    }
  }

  async function exchange() {
    if (!ticket || !initData) {
      showError("This page needs a launch from More → Mini App in Telegram.");
      return;
    }
    showError("Verifying Telegram launch…");
    try {
      const response = await api("/api/session", {
        method: "POST", headers: { "Content-Type": "text/plain", "X-Launch-Ticket": ticket }, body: initData,
      });
      session = response.token;
      ui.launch.hidden = true;
      ui.app.hidden = false;
      ui.logout.hidden = false;
      showError("");
      await load();
    } catch (error) {
      expire(error.message || "Launch verification failed. Reopen from Telegram.");
    }
  }

  document.getElementById("tabs").addEventListener("click", (event) => {
    const button = event.target.closest("button[data-tab]");
    if (!button || loading) return;
    activeTab = button.dataset.tab;
    nextBefore = null;
    ui.title.textContent = labels[activeTab];
    for (const tab of document.querySelectorAll("button[data-tab]")) tab.classList.toggle("active", tab === button);
    ui.more.hidden = true;
    load();
  });
  ui.refresh.addEventListener("click", () => { nextBefore = null; load(); });
  ui.more.addEventListener("click", () => load(true));
  ui.logout.addEventListener("click", async () => {
    try { await api("/api/session", { method: "DELETE" }); } catch (_) { /* local close still applies */ }
    expire("Session closed. Reopen the Mini App from Telegram to start a new one.");
  });

  exchange();
})();
