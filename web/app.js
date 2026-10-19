(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
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
  };

  const login = $("login");
  const app = $("app");
  const errorBanner = $("error-banner");

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

  function setConnection(ok, text) {
    const el = $("connection");
    el.textContent = text;
    el.classList.toggle("ok", ok);
    el.classList.toggle("bad", !ok);
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

  function messageElement(message) {
    const article = document.createElement("article");
    article.className = "message";

    const head = document.createElement("div");
    head.className = "message-head";
    const author = document.createElement("span");
    author.className = "message-author";
    author.textContent = (message.from && message.from.name) || "unknown";
    const time = document.createElement("span");
    time.className = "message-time";
    time.textContent = new Date(message.ts).toLocaleString();
    head.appendChild(author);
    head.appendChild(time);

    const text = document.createElement("div");
    text.className = "message-text";
    text.textContent = message.text || "";

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
    box.appendChild(messageElement(message));
    if (message.seq) state.lastSeq = Math.max(state.lastSeq, message.seq);
    if (nearBottom) box.scrollTop = box.scrollHeight;
  }

  function renderRooms() {
    const list = $("room-list");
    list.replaceChildren();
    for (const room of state.rooms) {
      const li = document.createElement("li");
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = "#" + room.id;
      button.classList.toggle("active", room.id === state.room);
      button.addEventListener("click", () => selectRoom(room.id));
      li.appendChild(button);
      list.appendChild(li);
    }
  }

  function renderAgents() {
    const list = $("agent-list");
    list.replaceChildren();
    for (const agent of state.agents) {
      const li = document.createElement("li");
      const name = document.createElement("span");
      name.className = "agent-name";
      name.textContent = agent.name;
      const status = document.createElement("span");
      status.className = "agent-status " + (agent.status === "online" ? "online" : "offline");
      status.textContent = agent.status;
      li.appendChild(name);
      li.appendChild(status);
      list.appendChild(li);
    }
  }

  async function loadAgents() {
    try {
      const body = await api("/v1/agents");
      state.agents = body.agents || [];
      renderAgents();
    } catch (_) {
      // presence is non-critical
    }
  }

  async function selectRoom(roomID) {
    state.room = roomID;
    state.lastSeq = 0;
    const room = state.rooms.find((r) => r.id === roomID);
    $("room-title").textContent = "#" + roomID;
    $("room-topic").textContent = room && room.topic ? room.topic : "";
    $("messages").replaceChildren();
    renderRooms();
    try {
      const body = await api("/v1/rooms/" + encodeURIComponent(roomID) + "/messages?after_seq=0&limit=200");
      for (const message of body.messages || []) appendMessage(message);
      $("messages").scrollTop = $("messages").scrollHeight;
      setConnection(true, "connected");
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
      await poll();
    } catch (err) {
      showError(err.message || String(err));
    } finally {
      if (button) button.disabled = false;
    }
  }

  function startPolling() {
    if (state.timer) window.clearInterval(state.timer);
    state.timer = window.setInterval(poll, 3000);
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
