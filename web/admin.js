(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
  const i18n = window.AgentChatI18n;
  const t = (key, vars) => (i18n ? i18n.t(key, vars) : key);
  const state = { token: "", timer: null };

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
    const headers = Object.assign({}, options.headers || {}, {
      Authorization: "Bearer " + state.token,
    });
    if (typeof options.body === "string" && !headers["Content-Type"]) {
      headers["Content-Type"] = "application/json";
    }
    const res = await fetch(path, Object.assign({}, options, { headers }));
    if (res.status === 401) {
      logout("Admin token rejected");
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
      throw new Error(message || ("HTTP " + res.status));
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

  function showAgentToken(name, token) {
    const box = $("agent-token-result");
    box.replaceChildren();
    const title = document.createElement("div");
    title.textContent = t("admin.token_title", { name });
    const code = document.createElement("code");
    code.textContent = token;
    const warning = document.createElement("div");
    warning.className = "muted";
    warning.textContent = t("admin.token_warning");
    const copy = actionButton(t("admin.token_copy"), async () => {
      try {
        await navigator.clipboard.writeText(token);
        copy.textContent = t("admin.token_copied");
      } catch (_) {
        const range = document.createRange();
        range.selectNodeContents(code);
        const sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
      }
    });
    box.appendChild(title);
    box.appendChild(code);
    box.appendChild(copy);
    box.appendChild(warning);
    box.classList.remove("hidden");
  }

  async function loadAgents() {
    const body = await api("/v1/admin/agents");
    const agents = body.agents || [];
    const rows = agents.map((agent) => {
      const actions = document.createElement("span");
      actions.className = "admin-actions";
      if (agent.kind !== "federation" && agent.kind !== "system" && agent.kind !== "bridge") {
        const token = actionButton(t("admin.user_token"), async () => {
          if (!window.confirm(t("admin.token_confirm", { name: agent.name }))) return;
          try {
            const out = await api("/v1/admin/agents/" + encodeURIComponent(agent.id) + "/token", { method: "POST" });
            showAgentToken(agent.name, out.token);
            await refresh();
          } catch (err) {
            setStatus(err.message || String(err), false);
          }
        });
        actions.appendChild(token);
      }
      const revoke = actionButton(t("admin.revoke"), async () => {
        if (!window.confirm(t("admin.revoke_confirm", { name: agent.name }))) return;
        try {
          await api("/v1/admin/agents/" + encodeURIComponent(agent.id), { method: "DELETE" });
          await refresh();
        } catch (err) {
          setStatus(err.message || String(err), false);
        }
      }, true);
      actions.appendChild(revoke);
      return [agent.name, agent.kind, agent.status || "", actions];
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
      await Promise.all([loadInvites(), loadAgents(), loadAudit()]);
      setStatus(t("admin.connected"), true);
    } catch (err) {
      setStatus(err.message || String(err), false);
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
    await refresh();
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
