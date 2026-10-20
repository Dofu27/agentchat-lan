(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const i18n = window.AgentChatI18n;
  const t = (key, vars) => (i18n ? i18n.t(key, vars) : key);
  const state = { token: "", timer: null, retryKeys: new Map(), retrying: new Set() };

  function setStatus(text, ok) {
    const el = $("admin-status");
    el.textContent = text;
    el.classList.toggle("ok", !!ok);
    el.classList.toggle("bad", !ok);
  }

  function showError(message) {
    const el = $("admin-login-error");
    el.textContent = message || "";
  }

  async function api(path, options = {}) {
    const requestToken = state.token;
    const headers = Object.assign({}, options.headers || {}, {
      Authorization: "Bearer " + requestToken,
    });
    if (typeof options.body === "string" && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    const res = await fetch(path, Object.assign({}, options, { headers }));
    if (res.status === 401) {
      if (requestToken === state.token) logout(t("admin.connect_failed"));
      throw new Error("admin token rejected");
    }
    if (!res.ok) {
      const text = await res.text();
      let message = text;
      try {
        const body = JSON.parse(text);
        if (body && body.error && body.error.message) message = body.error.message;
      } catch (_) {
        // keep text
      }
      const error = new Error(message || ("HTTP " + res.status));
      error.status = res.status;
      throw error;
    }
    if (res.status === 204) return null;
    const type = res.headers.get("content-type") || "";
    return type.includes("application/json") ? res.json() : res.text();
  }

  function table(headers, rows) {
    const wrap = document.createElement("div");
    wrap.className = "admin-table-wrap";
    const el = document.createElement("table");
    el.className = "admin-table";
    const thead = document.createElement("thead");
    const hr = document.createElement("tr");
    for (const h of headers) {
      const th = document.createElement("th");
      th.textContent = h;
      hr.appendChild(th);
    }
    thead.appendChild(hr);
    el.appendChild(thead);
    const tbody = document.createElement("tbody");
    for (const cells of rows) {
      const tr = document.createElement("tr");
      for (const cell of cells) {
        const td = document.createElement("td");
        if (cell instanceof Node) td.appendChild(cell);
        else td.textContent = cell == null ? "" : String(cell);
        tr.appendChild(td);
      }
      tbody.appendChild(tr);
    }
    el.appendChild(tbody);
    wrap.appendChild(el);
    return wrap;
  }

  function actionButton(label, onClick, danger) {
    const b = document.createElement("button");
    b.type = "button";
    b.textContent = label;
    if (danger) b.className = "danger";
    b.addEventListener("click", onClick);
    return b;
  }

  async function loadInvites() {
    const body = await api("/v1/admin/invites");
    const invites = Array.isArray(body) ? body : body.invites || [];
    const rows = invites.map((inv) => [
      inv.room || inv.room_id || "",
      inv.name || "",
      inv.expires_at || "",
      String(inv.uses_left ?? inv.uses ?? ""),
    ]);
    $("invite-list").replaceChildren(table([
      t("admin.room"), t("admin.name"), t("admin.expires"), t("admin.uses"),
    ], rows));
  }

  async function loadAgents() {
    const body = await api("/v1/admin/agents");
    const agents = body.agents || [];
    const rows = agents.map((agent) => {
      const button = actionButton(t("admin.revoke"), async () => {
        if (!window.confirm(t("admin.revoke_confirm", { name: agent.name }))) return;
        try {
          await api("/v1/admin/agents/" + encodeURIComponent(agent.id), { method: "DELETE" });
          await refresh();
        } catch (err) {
          setStatus(err.message || String(err), false);
        }
      }, true);
      return [agent.name, agent.kind, agent.status || "", button];
    });
    $("agent-list-admin").replaceChildren(table([
      t("admin.name"), t("admin.kind"), t("admin.status"), "",
    ], rows));
  }

  async function loadAudit() {
    const body = await api("/v1/admin/audit?limit=50");
    const events = Array.isArray(body) ? body : body.events || body.audit || [];
    const rows = events.map((e) => [
      e.ts || "",
      e.action || "",
      e.actor_id || e.actor || "",
      JSON.stringify(e.detail || {}).slice(0, 180),
    ]);
    $("audit-list").replaceChildren(table([
      t("admin.time"), t("admin.action"), t("admin.actor"), t("admin.detail"),
    ], rows));
  }

  async function refresh() {
    setStatus(t("admin.loading"), false);
    try {
      await Promise.all([loadInvites(), loadAgents(), loadAudit(), loadFederation()]);
      setStatus(t("admin.connected"), true);
      return true;
    } catch (err) {
      setStatus(err.message || String(err), false);
      return false;
    }
  }

  function retryKey() {
    const bytes = new Uint8Array(16);
    window.crypto.getRandomValues(bytes);
    return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
  }

  async function loadFederation() {
    const token = state.token;
    try {
      const status = await api("/v1/admin/federation");
      const outbox = await api("/v1/admin/federation/outbox?state=dead&limit=50");
      if (!token || token !== state.token) return;
      $("federation-status").textContent = t("fed.enabled");
      const rows = [[t("fed.node"), status.node_id || ""]];
      for (const peer of status.peers || []) rows.push([t("fed.peer"), peer.node_id + " (" + t(peer.enabled ? "fed.active" : "fed.inactive") + ")"]);
      for (const link of status.room_links || []) {
        rows.push([t("fed.link"), link.local_room + " → " + link.peer_id + "/" + link.remote_room + " (" + t(link.enabled ? "fed.active" : "fed.inactive") + ")"]);
      }
      for (const name of ["pending", "inflight", "delivered", "dead", "attempts"]) {
        rows.push([t("fed." + name), (status.outbox || {})[name] ?? 0]);
      }
      $("federation-summary").replaceChildren(table([t("fed.field"), t("fed.value")], rows));
      const jobs = outbox.items || [];
      const jobRows = jobs.map((job) => {
        const button = actionButton(t("fed.retry"), async () => {
          if (state.retrying.has(job.id)) return;
          const retryToken = state.token;
          state.retrying.add(job.id);
          button.disabled = true;
          try {
            if (!state.retryKeys.has(job.id)) state.retryKeys.set(job.id, retryKey());
            await api("/v1/admin/federation/outbox/" + encodeURIComponent(job.id) + "/retry", {
              method: "POST", headers: { "Idempotency-Key": state.retryKeys.get(job.id) },
            });
            state.retryKeys.delete(job.id);
            if (retryToken === state.token) await refresh();
          } catch (err) {
            if (err.status === 404 || err.status === 409) state.retryKeys.delete(job.id);
            if (retryToken === state.token) $("federation-status").textContent = t("fed.retry_failed");
          } finally {
            state.retrying.delete(job.id);
            button.disabled = false;
          }
        });
        button.disabled = state.retrying.has(job.id);
        return [job.id, job.peer_id, job.origin_node_id + "/" + job.origin_message_id,
          job.attempts, job.last_error, job.created_at, button];
      });
      $("federation-outbox").replaceChildren(jobs.length ? table([
        t("fed.id"), t("fed.peer"), t("fed.origin"), t("fed.attempts"),
        t("fed.error"), t("admin.time"), "",
      ], jobRows) : document.createTextNode(t("fed.empty")));
    } catch (err) {
      if (!token || token !== state.token) return;
      $("federation-summary").replaceChildren();
      $("federation-outbox").replaceChildren();
      $("federation-status").textContent = t(err.status === 404 ? "fed.disabled" : "fed.failed");
      if (err.status !== 404) throw err;
    }
  }

  async function createInvite(event) {
    event.preventDefault();
    $("invite-result").textContent = "";
    try {
      const body = {
        room: $("invite-room").value.trim() || "general",
        uses: Number($("invite-uses").value) || 1,
        ttl_seconds: (Number($("invite-ttl").value) || 60) * 60,
      };
      const out = await api("/v1/admin/invites", { method: "POST", body: JSON.stringify(body) });
      $("invite-result").textContent = t("admin.invite_result", { code: out.code });
      await refresh();
    } catch (err) {
      setStatus(err.message || String(err), false);
    }
  }

  function logout(message) {
    setStatus(t("status.offline"), false);
    state.token = "";
    state.retryKeys.clear();
    state.retrying.clear();
    $("federation-status").textContent = "";
    $("federation-summary").replaceChildren();
    $("federation-outbox").replaceChildren();
    if (state.timer) window.clearInterval(state.timer);
    state.timer = null;
    try {
      sessionStorage.removeItem("agentchat_admin_token");
    } catch (_) {
      // ignore
    }
    $("admin-app").classList.add("hidden");
    $("admin-login").classList.remove("hidden");
    $("admin-token").value = "";
    if (message) showError(message);
  }

  async function boot() {
    if (!(await refresh()) || !state.token) throw new Error(t("admin.connect_failed"));
    $("admin-login").classList.add("hidden");
    $("admin-app").classList.remove("hidden");
    state.timer = window.setInterval(refresh, 15000);
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
      if (state.token) refresh();
    });
  }

  $("admin-login-form").addEventListener("submit", async (event) => {
    event.preventDefault();
    showError("");
    state.token = $("admin-token").value.trim();
    if (!state.token) return;
    try {
      await boot();
      sessionStorage.setItem("agentchat_admin_token", state.token);
    } catch (err) {
      state.token = "";
      showError(err.message || String(err));
    }
  });
  $("invite-form").addEventListener("submit", createInvite);
  $("admin-refresh").addEventListener("click", refresh);
  $("admin-logout").addEventListener("click", () => logout(""));

  try {
    const saved = sessionStorage.getItem("agentchat_admin_token");
    if (saved) {
      state.token = saved;
      boot().catch((err) => {
        state.token = "";
        sessionStorage.removeItem("agentchat_admin_token");
        showError(err.message || String(err));
      });
    }
  } catch (_) {
    // no session storage
  }
})();
