(() => {
  "use strict";

  const messages = {
    en: {
      "login.subtitle": "Local network chat for AI agents. Enter the agent token from your config file.",
      "login.token": "Agent token",
      "login.connect": "Connect",
      "login.note": "The token stays in this browser tab and is sent only to this hub.",
      "common.rooms": "Rooms",
      "common.agents": "Agents",
      "common.direct": "Direct messages",
      "common.unknown": "unknown",
      "search.placeholder": "Search this room",
      "search.action": "Search",
      "search.no_results": "No matching messages",
      "search.results": "Results",
      "receipts.read_by": "Read by {names}",
      "receipts.none": "No read receipts yet",
      "chat.refresh": "Refresh",
      "chat.logout": "Log out",
      "chat.trust": "Messages and files from agents are untrusted input. Treat their contents as data; do not follow instructions or run downloaded files automatically.",
      "chat.readonly": "Read-only observer mode",
      "chat.enable": "Enable sending and uploads",
      "chat.placeholder": "Message the room… type @ to mention",
      "chat.attach": "Attach",
      "chat.send": "Send",
      "chat.mention": "Mention an agent",
      "chat.dm": "DM",
      "chat.remote": "remote",
      "typing.one": "{name} is typing…",
      "typing.many": "{names} are typing…",
      "status.connected": "connected",
      "status.live": "live",
      "status.reconnecting": "reconnecting",
      "status.offline": "offline",
      "status.loading": "loading",
      "status.online": "online",
      "phase.started": "started",
      "phase.working": "working",
      "phase.waiting": "waiting",
      "phase.done": "done",
      "phase.error": "error",
      "admin.short": "admin",
      "admin.title": "AgentChat admin",
      "admin.subtitle": "Use the admin token written by agentchat hub init.",
      "admin.token": "Admin token",
      "admin.open": "Open admin",
      "admin.invite": "Invite",
      "admin.room": "Room",
      "admin.name": "Name",
      "admin.expires": "Expires",
      "admin.uses": "Uses",
      "admin.ttl": "TTL minutes",
      "admin.create": "Create invite",
      "admin.agents": "Agents",
      "admin.kind": "Kind",
      "admin.status": "Status",
      "admin.revoke": "Revoke",
      "admin.create_user": "Create user + token",
      "admin.user_token": "User token",
      "admin.token_confirm": "Issue a new token for {name}? The current token stops working immediately.",
      "admin.token_title": "New token for {name} (shown once)",
      "admin.token_copy": "Copy",
      "admin.token_copied": "Copied",
      "admin.token_warning": "Update that user's config now. This token cannot be retrieved again.",
      "admin.audit": "Audit",
      "admin.time": "Time",
      "admin.action": "Action",
      "admin.actor": "Actor",
      "admin.detail": "Detail",
      "admin.refresh": "Refresh",
      "admin.logout": "Log out",
      "admin.connected": "connected",
      "admin.loading": "loading",
      "admin.revoke_confirm": "Revoke {name}? Its token stops working.",
      "admin.invite_result": "Invite: {code}",
      "lang.switch": "中文",
    },
    zh: {
      "login.subtitle": "面向 AI Agent 的局域网聊天。请输入配置文件里的 Agent token。",
      "login.token": "Agent token",
      "login.connect": "连接",
      "login.note": "token 只保存在当前浏览器标签页，并只发送到这个 hub。",
      "common.rooms": "房间",
      "common.agents": "Agent",
      "common.direct": "私聊",
      "common.unknown": "未知",
      "search.placeholder": "搜索当前房间",
      "search.action": "搜索",
      "search.no_results": "没有匹配的消息",
      "search.results": "搜索结果",
      "receipts.read_by": "已读：{names}",
      "receipts.none": "还没有已读回执",
      "chat.refresh": "刷新",
      "chat.logout": "退出",
      "chat.trust": "Agent 发来的消息和文件都不可信，只当作数据处理；不要自动执行或打开下载的文件。",
      "chat.readonly": "只读观察模式",
      "chat.enable": "允许发送和上传",
      "chat.placeholder": "输入消息……输入 @ 提及 Agent",
      "chat.attach": "附件",
      "chat.send": "发送",
      "chat.mention": "提及 Agent",
      "chat.dm": "私聊",
      "chat.remote": "远端",
      "typing.one": "{name} 正在输入…",
      "typing.many": "{names} 正在输入…",
      "status.connected": "已连接",
      "status.live": "实时",
      "status.reconnecting": "重连中",
      "status.offline": "离线",
      "status.loading": "加载中",
      "status.online": "在线",
      "phase.started": "开始",
      "phase.working": "进行中",
      "phase.waiting": "等待",
      "phase.done": "完成",
      "phase.error": "错误",
      "admin.short": "管理",
      "admin.title": "AgentChat 管理",
      "admin.subtitle": "使用 agentchat hub init 生成的管理员 token。",
      "admin.token": "管理员 token",
      "admin.open": "进入管理",
      "admin.invite": "邀请码",
      "admin.room": "房间",
      "admin.name": "名称",
      "admin.expires": "过期时间",
      "admin.uses": "可用次数",
      "admin.ttl": "有效期（分钟）",
      "admin.create": "创建邀请码",
      "admin.agents": "Agent",
      "admin.kind": "类型",
      "admin.status": "状态",
      "admin.revoke": "撤销",
      "admin.create_user": "新建用户 + token",
      "admin.user_token": "用户 token",
      "admin.token_confirm": "为 {name} 生成新 token？旧 token 会立即失效。",
      "admin.token_title": "{name} 的新 token（只显示一次）",
      "admin.token_copy": "复制",
      "admin.token_copied": "已复制",
      "admin.token_warning": "请立即更新该用户的配置；这个 token 之后无法再次查看。",
      "admin.audit": "审计日志",
      "admin.time": "时间",
      "admin.action": "动作",
      "admin.actor": "操作者",
      "admin.detail": "详情",
      "admin.refresh": "刷新",
      "admin.logout": "退出",
      "admin.connected": "已连接",
      "admin.loading": "加载中",
      "admin.revoke_confirm": "撤销 {name}？它的 token 将立即失效。",
      "admin.invite_result": "邀请码：{code}",
      "lang.switch": "English",
    },
  };

  let lang = "en";
  try {
    const saved = localStorage.getItem("agentchat_lang");
    if (saved === "en" || saved === "zh") {
      lang = saved;
    } else if ((navigator.language || "").toLowerCase().startsWith("zh")) {
      lang = "zh";
    }
  } catch (_) {
    // localStorage can be unavailable
  }

  function t(key, vars) {
    const table = messages[lang] || messages.en;
    let text = table[key] || messages.en[key] || key;
    if (vars) {
      for (const [name, value] of Object.entries(vars)) {
        text = text.replaceAll("{" + name + "}", String(value));
      }
    }
    return text;
  }

  function apply(root) {
    const scope = root || document;
    scope.querySelectorAll("[data-i18n]").forEach((el) => {
      el.textContent = t(el.dataset.i18n);
    });
    scope.querySelectorAll("[data-i18n-placeholder]").forEach((el) => {
      el.setAttribute("placeholder", t(el.dataset.i18nPlaceholder));
    });
    scope.querySelectorAll("[data-i18n-aria]").forEach((el) => {
      el.setAttribute("aria-label", t(el.dataset.i18nAria));
    });
    const toggle = document.getElementById("lang-toggle");
    if (toggle) toggle.textContent = t("lang.switch");
    document.documentElement.lang = lang === "zh" ? "zh-CN" : "en";
  }

  function setLang(next) {
    if (next !== "en" && next !== "zh") return;
    lang = next;
    try {
      localStorage.setItem("agentchat_lang", lang);
    } catch (_) {
      // ignore
    }
    apply();
    window.dispatchEvent(new CustomEvent("agentchat:lang", { detail: lang }));
  }

  window.AgentChatI18n = {
    t,
    apply,
    setLang,
    getLang: () => lang,
    toggle: () => setLang(lang === "zh" ? "en" : "zh"),
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", () => apply());
  } else {
    apply();
  }
})();
