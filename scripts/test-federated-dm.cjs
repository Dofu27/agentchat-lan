// Optional browser regression. Isolated loopback assets and synthetic API only.
// npm install playwright; BROWSER_EXECUTABLE=... node scripts/test-federated-dm.cjs
const assert = require("node:assert/strict");
const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("playwright");

(async () => {
  const assets = new Map([["/", ["index.html", "text/html"]],
    ["/static/app.js", ["app.js", "text/javascript"]],
    ["/static/i18n.js", ["i18n.js", "text/javascript"]],
    ["/static/style.css", ["style.css", "text/css"]]]);
  const server = http.createServer(async (req, res) => {
    const asset = assets.get(req.url);
    if (!asset) return res.writeHead(404).end();
    res.writeHead(200, { "Content-Type": asset[1] });
    res.end(await fs.readFile(path.join(__dirname, "..", "web", asset[0])));
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.BROWSER_EXECUTABLE ? { executablePath: process.env.BROWSER_EXECUTABLE } : {}) });
    const page = await browser.newPage({ locale: "en-US" });
    const errors = [], requests = [];
    page.on("pageerror", (err) => errors.push(err.message));
    const unsafe = '<img src=x onerror="window.injected=true">';
    const rooms = [{ id: "general", name: "general", kind: "room" },
      { id: "fdm_test", name: "remote-person@peer-" + unsafe, kind: "federated_dm", topic: "person@local / remote-person@peer" }];
    await page.route("**/v1/**", async (route) => {
      const req = route.request(), url = new URL(req.url());
      requests.push({ path: url.pathname, method: req.method(), body: req.postData() });
      const json = (value) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify(value) });
      if (req.headers().authorization !== "Bearer fixture-token") return route.fulfill({ status: 401, body: "unauthorized" });
      if (url.pathname === "/v1/me") return json({ agent: { id: "person", name: "person", kind: "human" }, rooms: rooms.map((r) => r.id) });
      if (url.pathname === "/v1/rooms") return json({ rooms });
      if (url.pathname === "/v1/agents") return json({ agents: [] });
      if (url.pathname.endsWith("/events")) return route.fulfill({ status: 200, contentType: "text/event-stream", body: "event: ready\ndata: {}\n\n" });
      if (url.pathname.endsWith("/messages")) return json(req.method() === "POST" ? { id: "sent", room: "fdm_test", seq: 1 } : { messages: [], has_more: false });
      if (url.pathname.endsWith("/receipts")) return json({ receipts: [] });
      if (url.pathname === "/v1/ack") return route.fulfill({ status: 204 });
      return route.fulfill({ status: 404 });
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.evaluate(() => window.AgentChatI18n.setLang("en"));
    await page.locator("#token").fill("fixture-token");
    await page.locator('#login-form button[type="submit"]').click();
    await page.locator("#dm-list button").waitFor();
    await page.locator("#dm-list button").click();
    assert.match(await page.locator("#room-title").textContent(), /Cross-hub DM|跨 hub 私聊/);
    assert.match(await page.locator("#room-topic").textContent(), /Text only|仅文本/);
    assert.equal(await page.locator("#file-input").isDisabled(), true);
    assert.equal(await page.locator("#room-title img").count(), 0);
    assert.equal(await page.evaluate(() => !!window.injected), false);
    await page.locator("#enable-writing").click();
    await page.locator("#message-text").fill("private text");
    await page.locator('#composer button[type="submit"]').click();
    await page.waitForFunction(() => document.getElementById("message-text").value === "");
    assert.equal(requests.filter((r) => r.path === "/v1/rooms/fdm_test/messages" && r.method === "POST").length, 1);
    assert.equal(requests.some((r) => r.path === "/v1/rooms/fdm_test/typing" || r.path === "/v1/rooms/fdm_test/receipts" || r.path === "/v1/files"), false);
    await page.locator("#lang-toggle").click();
    await page.waitForFunction(() => /跨 hub 私聊/.test(document.getElementById("room-title").textContent));
    assert.match(await page.locator("#room-topic").textContent(), /非端到端加密/);
    assert.equal(errors.length, 0, errors.join("\n"));
    console.log("federated DM browser regression passed");
  } finally { if (browser) await browser.close(); await new Promise((resolve) => server.close(resolve)); }
})().catch((err) => { console.error(err); process.exitCode = 1; });
