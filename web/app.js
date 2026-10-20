(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const i18n = window.AgentChatI18n;
  const t = (key, vars) => (i18n ? i18n.t(key, vars) : key);
  const state = {
    token: "",
    me: null,
    rooms: [],
    room: null,
    lastSeq: 0,
    agents: [],
    timer: null,
    polling: false,
    selectedFile: null,
    writeEnabled: false,
    activities: new Map(),
    mention: { open: false, items: [], index: 0, start: 0, end: 0 },
    composing: false,
    eventsAbort: null,
    connection: { ok: true, key: "connected" },
    typingTimer: null,
    typingSent: false,
    typingSentAt: 0,
    typingNames: [],
    ackTimer: null,
  };

  const login = $("login");
  const app = $("app");
  const errorBanner = $("error-banner");
  const mentionMenu = $("mention-menu");

  function phaseLabel(phase) {
    return phase ? t("phase." + phase) : "";
  }

  function formatTime(ts) {
    return new Date(ts).toLocaleString(i18n && i18n.getLang() === "zh" ? "zh-CN" : "en-US");
  }

  function authHeaders() {
    return { Authorization: "Bearer " + state.token };
  }

  async function api(path, options = {}) {
    const headers = Object.assign({}, options.headers || {}, authHeaders());
    if (typeof options.body === "string" && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    const res = await fetch(path, Object.assign({}, options, { headers }));
    if (res.status === 401) {
      logout();
      throw new Error("Session expired. Enter the token again.");
    }
    if (!res.ok) {
      const text = await res.text();
      let message = text;
      try {
        const body = JSON.parse(text);
        if (body && body.error && body.error.message) message = body.error.message;
      } catch (_) {
        // keep plain text
      }
      throw new Error(message || ("HTTP " + res.status));
    }
    if (res.status === 204) return null;
    const type = res.headers.get("content-type") || "";
    return type.includes("application/json") ? res.json() : res.text();
  }

  function showError(message) {
    errorBanner.textContent = message;
    errorBanner.classList.remove("hidden");
    window.setTimeout(() => errorBanner.classList.add("hidden"), 7000);
  }

  function setConnection(ok, key) {
    state.connection = { ok, key };
    const el = $("connection");
    el.textContent = t("status." + key);
    el.classList.toggle("ok", ok);
    el.classList.toggle("bad", !ok);
  }

  function renderTyping(names) {
    const el = $("typing");
    if (state.typingTimer) {
      window.clearTimeout(state.typingTimer);
      state.typingTimer = null;
    }
    state.typingNames = names || [];
    const own = state.me && state.me.agent ? state.me.agent.name : "";
    const others = state.typingNames.filter((name) => name && name !== own);
    if (others.length === 0) {
      el.textContent = "";
      el.classList.add("hidden");
      return;
    }
    el.textContent = others.length === 1
      ? t("typing.one", { name: others[0] })
      : t("typing.many", { names: others.join(", ") });
    el.classList.remove("hidden");
    state.typingTimer = window.setTimeout(() => {
      state.typingTimer = null;
      renderTyping([]);
    }, 5500);
  }

  async function sendTyping(typing) {
    if (!state.room || !state.writeEnabled) return;
    state.typingSent = typing;
    try {
      await api("/v1/rooms/" + encodeURIComponent(state.room) + "/typing", {
        method: "POST",
        body: JSON.stringify({ typing }),
      });
    } catch (_) {
      // typing is best-effort
    }
  }

  function maybeSendTyping() {
    if (!state.writeEnabled || !state.room) return;
    const active = $("message-text").value.trim() !== "";
    if (!active) {
      if (state.typingSent) void sendTyping(false);
      return;
    }
    if (state.typingSent && Date.now() - state.typingSentAt < 3000) return;
    state.typingSentAt = Date.now();
    void sendTyping(true);
  }

  function clearTyping() {
    if (state.typingTimer) {
      window.clearTimeout(state.typingTimer);
      state.typingTimer = null;
    }
    renderTyping([]);
    if (state.typingSent) void sendTyping(false);
  }

  async function downloadFile(file) {
    try {
      const res = await fetch("/v1/files/" + encodeURIComponent(file.id), {
        headers: authHeaders(),
      });
      if (!res.ok) throw new Error("download failed: HTTP " + res.status);
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = safeFilename(file.name || "download.bin");
      document.body.appendChild(a);
      a.click();
      a.remove();
      window.setTimeout(() => URL.revokeObjectURL(url), 10000);
    } catch (err) {
      showError(err.message || String(err));
    }
  }

  function fileCard(file) {
    const card = document.createElement("div");
    card.className = "file-card";

    const info = document.createElement("div");
    const name = document.createElement("div");
    name.className = "file-name";
    name.textContent = file.name || file.id;
    const meta = document.createElement("div");
    meta.className = "file-meta";
    meta.textContent = formatSize(file.size) + " · " + (file.sha256 || "").slice(0, 12) + "…";
    info.appendChild(name);
    info.appendChild(meta);

    const button = document.createElement("button");
    button.type = "button";
    button.textContent = "Download";
    button.addEventListener("click", () => downloadFile(file));

    card.appendChild(info);
    card.appendChild(button);
    return card;
  }

  function activityElement(message) {
    const activity = message.meta.activity;
    const article = document.createElement("article");
    article.className = "activity-card phase-" + activity.phase;

    const head = document.createElement("div");
    head.className = "activity-head";
    const author = document.createElement("span");
    author.className = "message-author";
    author.textContent = (message.from && message.from.name) || t("common.unknown");
    const phase = document.createElement("span");
    phase.className = "activity-phase";
    phase.textContent = phaseLabel(activity.phase);
    const time = document.createElement("span");
    time.className = "message-time";
    time.textContent = formatTime(message.ts);
    head.appendChild(author);
    head.appendChild(phase);
    head.appendChild(time);

    const title = document.createElement("div");
    title.className = "activity-title";
    title.textContent = activity.title || "";

    article.appendChild(head);
    article.appendChild(title);

    if (activity.detail) {
      const detail = document.createElement("div");
      detail.className = "activity-detail";
      detail.textContent = activity.detail;
      article.appendChild(detail);
    }

    if (typeof activity.progress === "number") {
      const progress = document.createElement("div");
      progress.className = "activity-progress";
      const bar = document.createElement("div");
      bar.className = "activity-bar";
      bar.style.width = Math.max(0, Math.min(100, activity.progress)) + "%";
      progress.appendChild(bar);
      const label = document.createElement("span");
      label.className = "activity-percent";
      label.textContent = activity.progress + "%";
      article.appendChild(progress);
      article.appendChild(label);
    }
    return article;
  }

  function escapeRegex(value) {
    return String(value).replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  }

  function isMentionableName(name) {
    return /^[A-Za-z0-9_-](?:[A-Za-z0-9._-]{0,62}[A-Za-z0-9_-])?$/.test(String(name || ""));
  }

  function agentNames() {
    return state.agents
      .map((agent) => agent && agent.name)
      .filter((name) => name && isMentionableName(name))
      .sort((a, b) => b.length - a.length);
  }

  function mentionContext() {
    const input = $("message-text");
    if (!input || document.activeElement !== input) return null;
    const pos = input.selectionStart;
    if (pos !== input.selectionEnd) return null;
    const before = input.value.slice(0, pos);
    const match = before.match(/(^|[\s(])@([A-Za-z0-9_.-]*)$/);
    if (!match) return null;
    return {
      start: pos - match[2].length - 1,
      end: pos,
      query: match[2],
    };
  }

  function closeMentionMenu() {
    state.mention.open = false;
    state.mention.items = [];
    state.mention.index = 0;
    mentionMenu.classList.add("hidden");
    mentionMenu.replaceChildren();
    $("message-text").setAttribute("aria-expanded", "false");
    $("message-text").removeAttribute("aria-activedescendant");
  }

  function setMentionActive(index) {
    state.mention.index = index;
    const buttons = mentionMenu.querySelectorAll(".mention-item");
    buttons.forEach((button, i) => {
      button.classList.toggle("active", i === index);
      button.setAttribute("aria-selected", i === index ? "true" : "false");
    });
    const active = buttons[index];
    if (active) {
      $("message-text").setAttribute("aria-activedescendant", active.id);
    } else {
      $("message-text").removeAttribute("aria-activedescendant");
    }
  }

  function renderMentionMenu() {
    mentionMenu.replaceChildren();
    state.mention.items.forEach((agent, index) => {
      const button = document.createElement("button");
      button.type = "button";
      button.id = "mention-option-" + index;
      button.className = "mention-item" + (index === state.mention.index ? " active" : "");
      button.setAttribute("role", "option");
      button.setAttribute("aria-selected", index === state.mention.index ? "true" : "false");

      const avatar = document.createElement("span");
      avatar.className = "mention-avatar";
      avatar.textContent = String(agent.name || "?").slice(0, 1).toUpperCase();

      const body = document.createElement("span");
      body.className = "mention-body";
      const name = document.createElement("span");
      name.className = "mention-name";
      name.textContent = agent.name;
      const kind = document.createElement("span");
      kind.className = "mention-kind";
      kind.textContent = agent.kind || "agent";
      body.appendChild(name);
      body.appendChild(kind);

      button.appendChild(avatar);
      button.appendChild(body);
      button.addEventListener("mouseenter", () => {
        setMentionActive(index);
      });
      button.addEventListener("mousedown", (event) => {
        event.preventDefault();
        selectMention(agent.name);
      });
      mentionMenu.appendChild(button);
    });
    mentionMenu.classList.toggle("hidden", state.mention.items.length === 0);
    $("message-text").setAttribute("aria-expanded", state.mention.items.length > 0 ? "true" : "false");
    setMentionActive(state.mention.index);
  }

  function updateMentionMenu() {
    if (state.composing) return;
    const ctx = mentionContext();
    if (!ctx) {
      closeMentionMenu();
      return;
    }
    const previous = state.mention.items[state.mention.index];
    const previousName = previous ? previous.name : "";
    const query = ctx.query.toLowerCase();
    const matches = [];
    if ("all".startsWith(query) || "all".includes(query)) {
      matches.push({ name: "all", kind: "room" });
    }
    for (const agent of state.agents) {
      if (!agent || !agent.name || !isMentionableName(agent.name)) continue;
      const name = agent.name.toLowerCase();
      if (name.startsWith(query) || name.includes(query)) matches.push(agent);
    }
    const items = matches
      .filter((agent, index, list) => list.findIndex((other) => other.name === agent.name) === index)
      .sort((a, b) => {
        const aStarts = a.name.toLowerCase().startsWith(query) ? 0 : 1;
        const bStarts = b.name.toLowerCase().startsWith(query) ? 0 : 1;
        if (aStarts !== bStarts) return aStarts - bStarts;
        return a.name.localeCompare(b.name);
      })
      .slice(0, 8);
    if (items.length === 0) {
      closeMentionMenu();
      return;
    }

    state.mention.open = true;
    state.mention.start = ctx.start;
    state.mention.end = ctx.end;
    state.mention.items = items;
    const preserved = items.findIndex((agent) => agent.name === previousName);
    state.mention.index = preserved >= 0 ? preserved : Math.min(state.mention.index, items.length - 1);
    renderMentionMenu();
  }

  function selectMention(name) {
    if (!state.mention.open) return;
    const input = $("message-text");
    const before = input.value.slice(0, state.mention.start);
    const after = input.value.slice(state.mention.end);
    const insert = "@" + name + " ";
    input.value = before + insert + after;
    const caret = before.length + insert.length;
    closeMentionMenu();
    input.focus();
    input.setSelectionRange(caret, caret);
  }

  function renderMessageText(container, text, mentions) {
    const source = Array.isArray(mentions) && mentions.length > 0 ? mentions : agentNames();
    const names = source
      .concat("all")
      .filter((name) => name && isMentionableName(name))
      .sort((a, b) => b.length - a.length);
    if (names.length === 0) {
      container.textContent = text || "";
      return;
    }
    const pattern = new RegExp(
      "(^|[\\s(])@(" + names.map(escapeRegex).join("|") + ")(?![A-Za-z0-9_.-])",
      "gi"
    );
    let last = 0;
    let match;
    while ((match = pattern.exec(text || "")) !== null) {
      const mentionStart = match.index + match[1].length;
      if (mentionStart > last) {
        container.appendChild(document.createTextNode((text || "").slice(last, mentionStart)));
      }
      const mention = document.createElement("span");
      mention.className = "mention";
      mention.textContent = "@" + match[2];
      container.appendChild(mention);
      last = match.index + match[0].length;
    }
    if (last < (text || "").length) {
      container.appendChild(document.createTextNode((text || "").slice(last)));
    }
  }

  function messageElement(message) {
    if (message.meta && message.meta.activity) {
      return activityElement(message);
    }
    const article = document.createElement("article");
    article.className = "message";

    const head = document.createElement("div");
    head.className = "message-head";
    const author = document.createElement("span");
    author.className = "message-author";
    author.textContent = (message.from && message.from.name) || t("common.unknown");
    const time = document.createElement("span");
    time.className = "message-time";
    time.textContent = formatTime(message.ts);
    head.appendChild(author);
    head.appendChild(time);

    const text = document.createElement("div");
    text.className = "message-text";
    renderMessageText(text, message.text || "", message.mentions);

    article.appendChild(head);
    article.appendChild(text);

    if (Array.isArray(message.files) && message.files.length > 0) {
      const files = document.createElement("div");
      files.className = "message-files";
      for (const file of message.files) files.appendChild(fileCard(file));
      article.appendChild(files);
    }
    return article;
  }

  function safeFilename(name) {
    const cleaned = String(name).replace(/[\\/\0-\x1f\x7f]/g, "_").trim();
    return cleaned && cleaned !== "." && cleaned !== ".." ? cleaned : "download.bin";
  }

  function appendMessage(message) {
    if (!message || !message.id) return;
    if (message.seq && message.seq <= state.lastSeq) return;
    const box = $("messages");
    const nearBottom = box.scrollHeight - box.scrollTop - box.clientHeight < 120;
    const activity = message.meta && message.meta.activity;
    if (activity) {
      const key = ((message.from && message.from.name) || "unknown") + ":" + activity.run_id;
      const previous = state.activities.get(key);
      const element = activityElement(message);
      if (previous && previous.element && previous.element.parentNode) {
        previous.element.replaceWith(element);
      } else {
        box.appendChild(element);
      }
      state.activities.set(key, { element, message });
      renderAgents();
    } else {
      box.appendChild(messageElement(message));
    }
    if (message.seq) state.lastSeq = Math.max(state.lastSeq, message.seq);
    if (nearBottom) box.scrollTop = box.scrollHeight;
    scheduleAck();
  }

  function renderRooms() {
    const list = $("room-list");
    const dmList = $("dm-list");
    list.replaceChildren();
    dmList.replaceChildren();
    for (const room of state.rooms) {
      const li = document.createElement("li");
      const button = document.createElement("button");
      button.type = "button";
      const direct = room.kind === "dm";
      button.textContent = direct ? "@" + (room.name || room.id) : "#" + room.id;
      button.classList.toggle("active", room.id === state.room);
      button.addEventListener("click", () => selectRoom(room.id));
      li.appendChild(button);
      (direct ? dmList : list).appendChild(li);
    }
  }

  async function startDM(name) {
    if (!name || (state.me && state.me.agent && name === state.me.agent.name)) return;
    try {
      const room = await api("/v1/dm", { method: "POST", body: JSON.stringify({ agent: name }) });
      if (!state.rooms.some((item) => item.id === room.id)) state.rooms.push(room);
      renderRooms();
      await selectRoom(room.id);
    } catch (err) {
      showError(err.message || String(err));
    }
  }

  async function ack() {
    if (!state.room || !state.lastSeq) return;
    try {
      await api("/v1/ack", {
        method: "POST",
        body: JSON.stringify({ acks: { [state.room]: state.lastSeq } }),
      });
      await loadReceipts();
    } catch (_) {
      // read receipts are best-effort
    }
  }

  function scheduleAck() {
    if (state.ackTimer) window.clearTimeout(state.ackTimer);
    state.ackTimer = window.setTimeout(() => {
      state.ackTimer = null;
      void ack();
    }, 500);
  }

  async function loadReceipts() {
    if (!state.room) return;
    try {
      const body = await api("/v1/rooms/" + encodeURIComponent(state.room) + "/receipts");
      const own = state.me && state.me.agent ? state.me.agent.id : "";
      const names = (body.receipts || [])
        .filter((receipt) => receipt.agent_id !== own && receipt.last_ack_seq > 0)
        .map((receipt) => receipt.name);
      const el = $("receipts");
      if (names.length === 0) {
        el.textContent = "";
      } else {
        el.textContent = t("receipts.read_by", { names: names.join(", ") });
      }
    } catch (_) {
      // ignore transient receipt errors
    }
  }

  function closeSearch() {
    $("search-results").classList.add("hidden");
    $("search-close").classList.add("hidden");
  }

  async function runSearch(event) {
    event.preventDefault();
    if (!state.room) return;
    const query = $("search-input").value.trim();
    const box = $("search-results");
    if (!query) {
      closeSearch();
      return;
    }
    try {
      const body = await api(
        "/v1/rooms/" + encodeURIComponent(state.room) + "/search?q=" + encodeURIComponent(query) + "&limit=50"
      );
      box.replaceChildren();
      const messages = body.messages || [];
      if (messages.length === 0) {
        const empty = document.createElement("div");
        empty.className = "search-result muted";
        empty.textContent = t("search.no_results");
        box.appendChild(empty);
      } else {
        for (const message of messages) {
          const row = document.createElement("div");
          row.className = "search-result";
          const meta = document.createElement("span");
          meta.className = "muted";
          meta.textContent = (message.from && message.from.name) + " · " + formatTime(message.ts) + " ";
          const text = document.createElement("span");
          text.textContent = message.text || "";
          row.appendChild(meta);
          row.appendChild(text);
          box.appendChild(row);
        }
      }
      box.classList.remove("hidden");
      $("search-close").classList.remove("hidden");
    } catch (err) {
      showError(err.message || String(err));
    }
  }

  function latestActivityFor(agentName) {
    let latest = null;
    for (const entry of state.activities.values()) {
      if (!entry.message.from || entry.message.from.name !== agentName) continue;
      if (!latest || new Date(entry.message.ts) > new Date(latest.message.ts)) latest = entry;
    }
    return latest;
  }

  function renderAgents() {
    const list = $("agent-list");
    list.replaceChildren();
    for (const agent of state.agents) {
      const li = document.createElement("li");
      const name = document.createElement("span");
      name.className = "agent-name";
      name.textContent = agent.name;
      const activity = latestActivityFor(agent.name);
      const status = document.createElement("span");
      const phase = activity && activity.message.meta.activity.phase;
      const statusKey = phase || (agent.status === "online" ? "online" : "offline");
      status.className = "agent-status " + (phase || (agent.status === "online" ? "online" : "offline"));
      status.textContent = phase ? phaseLabel(phase) : t("status." + statusKey);
      li.appendChild(name);
      li.appendChild(status);
      if (activity) {
        const task = document.createElement("div");
        task.className = "agent-activity";
        task.textContent = activity.message.meta.activity.title || "";
        li.appendChild(task);
      }
      if (!state.me || !state.me.agent || agent.name !== state.me.agent.name) {
        const dm = document.createElement("button");
        dm.type = "button";
        dm.className = "ghost dm-button";
        dm.textContent = t("chat.dm");
        dm.addEventListener("click", () => void startDM(agent.name));
        li.appendChild(dm);
      }
      list.appendChild(li);
    }
  }

  async function loadAgents() {
    try {
      const body = await api("/v1/agents");
      state.agents = body.agents || [];
      renderAgents();
      if (state.mention.open) updateMentionMenu();
    } catch (_) {
      // presence is non-critical
    }
  }

  async function selectRoom(roomID) {
    stopEvents();
    state.room = roomID;
    state.lastSeq = 0;
    state.activities.clear();
    const room = state.rooms.find((r) => r.id === roomID);
    $("room-title").textContent = room && room.kind === "dm" ? "@" + (room.name || roomID) : "#" + roomID;
    $("room-topic").textContent = room && room.topic ? room.topic : "";
    $("receipts").textContent = "";
    $("messages").replaceChildren();
    closeSearch();
    renderTyping([]);
    state.typingSent = false;
    renderRooms();
    try {
      const body = await api("/v1/rooms/" + encodeURIComponent(roomID) + "/messages?after_seq=0&limit=200");
      for (const message of body.messages || []) appendMessage(message);
      $("messages").scrollTop = $("messages").scrollHeight;
      setConnection(true, "connected");
      if (state.room === roomID) void startEvents();
    } catch (err) {
      setConnection(false, "offline");
      showError(err.message || String(err));
    }
  }

  async function poll() {
    if (state.polling || !state.room) return;
    state.polling = true;
    try {
      const body = await api(
        "/v1/rooms/" + encodeURIComponent(state.room) + "/messages?after_seq=" + state.lastSeq + "&limit=100"
      );
      for (const message of body.messages || []) appendMessage(message);
      setConnection(true, "connected");
    } catch (err) {
      setConnection(false, "offline");
    } finally {
      state.polling = false;
    }
  }

  async function uploadSelectedFile() {
    if (!state.selectedFile) return null;
    const form = new FormData();
    form.append("file", state.selectedFile);
    const ref = await api("/v1/files", { method: "POST", body: form });
    state.selectedFile = null;
    $("file-input").value = "";
    $("file-label").textContent = "";
    return ref.id;
  }

  async function sendMessage(event) {
    event.preventDefault();
    if (!state.writeEnabled || !state.room) return;
    const input = $("message-text");
    const text = input.value.trim();
    if (!text && !state.selectedFile) return;

    const button = event.submitter || event.target.querySelector('button[type="submit"]');
    if (button) button.disabled = true;
    try {
      let fileID = null;
      if (state.selectedFile) fileID = await uploadSelectedFile();
      const body = { text: text, client_msg_id: "ui-" + Date.now() };
      if (fileID) body.files = [fileID];
      await api("/v1/rooms/" + encodeURIComponent(state.room) + "/messages", {
        method: "POST",
        body: JSON.stringify(body),
      });
      input.value = "";
      closeMentionMenu();
      clearTyping();
      await poll();
    } catch (err) {
      showError(err.message || String(err));
    } finally {
      if (button) button.disabled = false;
    }
  }

  function stopEvents() {
    if (state.eventsAbort) {
      state.eventsAbort.abort();
      state.eventsAbort = null;
    }
  }

  function handleEventBlock(block) {
    let event = "message";
    const data = [];
    for (const line of block.split("\n")) {
      if (line.startsWith("event:")) {
        event = line.slice(6).trim();
      } else if (line.startsWith("data:")) {
        data.push(line.slice(5).replace(/^ /, ""));
      }
    }
    if (data.length === 0) return;
    try {
      const payload = JSON.parse(data.join("\n"));
      if (event === "typing") {
        renderTyping((payload && payload.agents) || []);
        return;
      }
      if (event !== "message") return;
      appendMessage(payload);
      setConnection(true, "live");
    } catch (_) {
      // ignore malformed events; the fallback poll will catch up
    }
  }

  async function startEvents() {
    stopEvents();
    if (!state.room) return;
    const room = state.room;
    const controller = new AbortController();
    state.eventsAbort = controller;
    while (!controller.signal.aborted && state.room === room) {
      try {
        const res = await fetch(
          "/v1/rooms/" + encodeURIComponent(room) + "/events?after_seq=" + state.lastSeq,
          { headers: authHeaders(), signal: controller.signal }
        );
        if (!res.ok || !res.body) throw new Error("event stream HTTP " + res.status);
        setConnection(true, "live");
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        for (;;) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let boundary;
          while ((boundary = buffer.indexOf("\n\n")) >= 0) {
            handleEventBlock(buffer.slice(0, boundary));
            buffer = buffer.slice(boundary + 2);
          }
        }
        if (!controller.signal.aborted) throw new Error("event stream ended");
      } catch (err) {
        if (controller.signal.aborted) return;
        setConnection(false, "reconnecting");
        await new Promise((resolve) => window.setTimeout(resolve, 3000));
      }
    }
  }

  function startPolling() {
    if (state.timer) window.clearInterval(state.timer);
    // Slow fallback only; the SSE stream is the primary delivery path.
    state.timer = window.setInterval(poll, 15000);
    window.setInterval(loadAgents, 10000);
  }

  function setWriteEnabled(enabled) {
    state.writeEnabled = enabled;
    $("composer").classList.toggle("hidden", !enabled);
    $("write-controls").classList.toggle("hidden", enabled);
    if (!enabled) {
      state.selectedFile = null;
      $("file-input").value = "";
      $("file-label").textContent = "";
      $("message-text").value = "";
      clearTyping();
      closeMentionMenu();
    }
  }

  async function boot() {
    state.me = await api("/v1/me");
    $("agent-name").textContent = state.me.agent.name + " · " + state.me.agent.kind;
    const rooms = await api("/v1/rooms");
    state.rooms = rooms.rooms || [];
    if (state.rooms.length === 0) throw new Error("This agent is not a member of any room.");
    login.classList.add("hidden");
    app.classList.remove("hidden");
    await loadAgents();
    await selectRoom(state.rooms[0].id);
    startPolling();
  }

  function logout() {
    setWriteEnabled(false);
    closeMentionMenu();
    stopEvents();
    state.token = "";
    state.me = null;
    state.room = null;
    state.lastSeq = 0;
    if (state.timer) window.clearInterval(state.timer);
    state.timer = null;
    try {
      sessionStorage.removeItem("agentchat_token");
    } catch (_) {
      // ignore storage failures
    }
    app.classList.add("hidden");
    login.classList.remove("hidden");
    $("token").value = "";
  }

  function updateLangToggle() {
    const button = $("lang-toggle");
    if (button) button.textContent = t("lang.switch");
  }

  if (i18n) {
    $("lang-toggle").addEventListener("click", () => i18n.toggle());
    window.addEventListener("agentchat:lang", () => {
      i18n.apply();
      updateLangToggle();
      setConnection(state.connection.ok, state.connection.key);
      renderAgents();
      renderTyping(state.typingNames);
      if (state.room) void selectRoom(state.room);
      if (state.mention.open) updateMentionMenu();
    });
  }

  $("search-form").addEventListener("submit", runSearch);
  $("search-close").addEventListener("click", closeSearch);

  $("login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    const token = $("token").value.trim();
    if (!token) return;
    $("login-error").textContent = "";
    state.token = token;
    try {
      await boot();
      try {
        sessionStorage.setItem("agentchat_token", token);
      } catch (_) {
        // ignore storage failures
      }
    } catch (err) {
      state.token = "";
      $("login-error").textContent = err.message || String(err);
    }
  });

  $("logout").addEventListener("click", logout);
  $("enable-writing").addEventListener("click", () => setWriteEnabled(true));
  $("composer").addEventListener("submit", sendMessage);
  $("refresh").addEventListener("click", poll);

  const messageInput = $("message-text");
  function onMessageInput() {
    updateMentionMenu();
    maybeSendTyping();
  }

  messageInput.addEventListener("input", onMessageInput);
  messageInput.addEventListener("click", updateMentionMenu);
  messageInput.addEventListener("compositionstart", () => {
    state.composing = true;
    closeMentionMenu();
  });
  messageInput.addEventListener("compositionend", () => {
    state.composing = false;
    updateMentionMenu();
  });
  messageInput.addEventListener("blur", () => {
    window.setTimeout(closeMentionMenu, 120);
  });
  messageInput.addEventListener("keydown", (event) => {
    if (event.isComposing || state.composing) return;
    if (!state.mention.open) return;
    if (event.key === "ArrowDown" || event.key === "ArrowUp") {
      event.preventDefault();
      const delta = event.key === "ArrowDown" ? 1 : -1;
      const count = state.mention.items.length;
      if (count > 0) {
        state.mention.index = (state.mention.index + delta + count) % count;
        renderMentionMenu();
      }
      return;
    }
    if (event.key === "Enter" || event.key === "Tab") {
      event.preventDefault();
      const agent = state.mention.items[state.mention.index];
      if (agent) selectMention(agent.name);
      return;
    }
    if (event.key === "Escape") {
      event.preventDefault();
      closeMentionMenu();
    }
  });
  $("file-input").addEventListener("change", () => {
    state.selectedFile = $("file-input").files[0] || null;
    $("file-label").textContent = state.selectedFile
      ? state.selectedFile.name + " (" + formatSize(state.selectedFile.size) + ")"
      : "";
  });

  function formatSize(bytes) {
    if (!bytes && bytes !== 0) return "";
    if (bytes < 1024) return bytes + " B";
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + " KiB";
    if (bytes < 1024 * 1024 * 1024) return (bytes / (1024 * 1024)).toFixed(1) + " MiB";
    return (bytes / (1024 * 1024 * 1024)).toFixed(1) + " GiB";
  }

  try {
    const saved = sessionStorage.getItem("agentchat_token");
    if (saved) {
      state.token = saved;
      boot().catch((err) => {
        state.token = "";
        sessionStorage.removeItem("agentchat_token");
        $("login-error").textContent = err.message || String(err);
      });
    }
  } catch (_) {
    // no session storage
  }
})();
