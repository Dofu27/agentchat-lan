// Optional browser regression: npm install playwright; node scripts/test-admin-federation.cjs
// Uses only loopback static files and mock API responses; no hub or credentials.
const assert = require("node:assert/strict");
const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("playwright");

(async () => {
  const assets = new Map([
    ["/admin", ["admin.html", "text/html"]],
    ["/static/admin.js", ["admin.js", "text/javascript"]],
    ["/static/i18n.js", ["i18n.js", "text/javascript"]],
    ["/static/style.css", ["style.css", "text/css"]],
  ]);
  const server = http.createServer(async (req, res) => {
    const asset = assets.get(req.url);
    if (!asset) { res.writeHead(404).end(); return; }
    try {
      res.writeHead(200, { "Content-Type": asset[1] });
      res.end(await fs.readFile(path.join(__dirname, "..", "web", asset[0])));
    } catch (_) { res.end(); }
  });
  await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
  let browser;
  try {
    browser = await chromium.launch({ headless: true,
      ...(process.env.BROWSER_EXECUTABLE ? { executablePath: process.env.BROWSER_EXECUTABLE } : {}) });
    const page = await browser.newPage();
    const errors = [];
    page.on("pageerror", (e) => errors.push(e.message));
    let enabled = false, dead = true, failAck = true;
    const keys = [];
    const canary = "fixture-secret-must-not-render";
    const unsafe = '<img src=x onerror="window.injected=true">';
    await page.route("**/v1/admin/**", async (route) => {
      const req = route.request();
      const pathname = new URL(req.url()).pathname;
      const json = (body, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
      if (req.headers().authorization !== "Bearer fixture-admin") return json({ error: { message: "unauthorized" } }, 401);
      if (pathname.startsWith("/v1/admin/federation")) {
        if (!enabled) return json({ error: { message: "not found" } }, 404);
        if (pathname.endsWith("/retry")) {
          assert.equal(req.method(), "POST");
          keys.push(req.headers()["idempotency-key"]);
          if (failAck) { failAck = false; return route.abort("failed"); }
          dead = false;
          return json({ id: "job-test", replayed: true });
        }
        if (pathname.endsWith("/outbox")) return json({ items: dead ? [{ id: "job-test", peer_id: "node-peer",
          origin_node_id: "node-local", origin_message_id: "message", attempts: 5,
          last_error: unsafe, created_at: "2026-10-20T12:00:00Z" }] : [] });
        return json({ runtime_enabled: true, node_id: unsafe, secret: canary, payload: canary, peers: [{ node_id: "node-peer", enabled: true }],
          room_links: [{ local_room: "general", peer_id: "node-peer", remote_room: "other" }],
          outbox: { pending: 0, inflight: 0, delivered: 0, dead: dead ? 1 : 0, attempts: 5 } });
      }
      if (pathname.endsWith("/agents")) return json({ agents: [] });
      return json([]);
    });
    await page.goto(`http://127.0.0.1:${server.address().port}/admin`);
    // Choose English deterministically, independent of browser locale.
    await page.evaluate(() => { localStorage.setItem("agentchat_lang", "en"); });
    await page.reload();
    await page.locator("#admin-token").fill("fixture-invalid");
    await page.locator("#admin-login-form button").click();
    await page.locator("#admin-login-error").filter({ hasText: /authenticate|无法认证/ }).waitFor();
    assert.equal(await page.locator("#admin-app").isVisible(), false);
    await page.locator("#admin-token").fill("fixture-admin");
    await page.locator("#admin-login-form button").click();
    await page.locator("#federation-status").filter({ hasText: /disabled|未启用/ }).waitFor();
    assert.match(await page.locator("#admin-status").innerText(), /connected|已连接/);
    enabled = true;
    await page.locator("#admin-refresh").click();
    const retry = page.locator("#federation-outbox button");
    await retry.waitFor();
    assert.equal(await page.locator("#federation-panel img").count(), 0);
    assert.equal(await page.evaluate(() => window.injected), undefined);
    assert.equal((await page.locator("body").innerText()).includes(canary), false);
    await retry.click();
    await page.locator("#federation-status").filter({ hasText: /not confirmed|未确认/ }).waitFor();
    await retry.click();
    await page.locator("#federation-outbox").filter({ hasText: /No dead letters|没有死信/ }).waitFor();
    assert.equal(keys.length, 2);
    assert.ok(keys[0]);
    assert.equal(keys[0], keys[1], "lost ACK must retain the idempotency key");
    const before = await page.locator("#federation-panel h2").innerText();
    await page.locator("#lang-toggle").click();
    await page.waitForFunction((text) => document.querySelector("#federation-panel h2").textContent !== text, before);
    assert.deepEqual(errors, []);
    await page.locator("#admin-logout").click();
    assert.equal(await page.locator("#federation-summary").innerText(), "");
    console.log("Admin federation UI: invalid auth, disabled, status, safe DOM, retry/lost ACK, i18n, logout passed");
  } finally {
    if (browser) await browser.close();
    await new Promise((resolve) => server.close(resolve));
  }
})().catch((err) => { console.error(err); process.exitCode = 1; });
