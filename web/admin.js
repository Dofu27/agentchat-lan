(() => {
  "use strict";

  const $ = (id) => document.getElementById(id);
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
    $("invite-list").replaceChildren(table(["Room", "Name", "Expires", "Uses"], rows));
  }

  async function loadAgents() {
    const body = await api("/v1/admin/agents");
    const agents = body.agents || [];
    const rows = agents.map((agent) => {
      const button = actionButton("Revoke", async () => {
        if (!window.confirm("Revoke " + agent.name + "? Its token stops working.")) return;
        try {
          await api("/v1/admin/agents/" + encodeURIComponent(agent.id), { method: "DELETE" });
          await refresh();
        } catch (err) {
          setStatus(err.message || String(err), false);
        }
      }, true);
      return [agent.name, agent.kind, agent.status || "", button];
    });
    $("agent-list-admin").replaceChildren(table(["Name", "Kind", "Status", ""], rows));
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
    $("audit-list").replaceChildren(table(["Time", "Action", "Actor", "Detail"], rows));
  }

  async function refresh() {
    setStatus("loading", false);
    try {
      await Promise.all([loadInvites(), loadAgents(), loadAudit()]);
      setStatus("connected", true);
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
      $("invite-result").textContent = "Invite: " + out.code;
      await refresh();
    } catch (err) {
      setStatus(err.message || String(err), false);
    }
  }

  function logout(message) {
    setStatus("offline", false);
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
