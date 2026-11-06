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
  const labels = { status: "Status", history: "History", documents: "Documents", links: "Saved links", tasks: "Tasks" };
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
      } else {
        const params = new URLSearchParams({ limit: "10" });
        if (append && nextBefore) params.set("before", String(nextBefore));
        const data = await api(`/api/${activeTab}?${params}`);
        nextBefore = data.next_before_id || null;
        renderItems(activeTab, data.items, append);
      }
      ui.more.hidden = activeTab === "status" || !nextBefore;
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
