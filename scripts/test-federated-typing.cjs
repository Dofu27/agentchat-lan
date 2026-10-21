// Isolated browser regression: loopback assets, synthetic API/credentials only.
const assert = require("node:assert/strict");
const http = require("node:http");
const fs = require("node:fs/promises");
const path = require("node:path");
const { chromium } = require("playwright");

(async () => {
  const streams = new Map(), requests = [];
  let latestRemote;
  const rooms = [{id:"general",name:"general",kind:"room"},
    {id:"fdm_test",name:"peer-person",kind:"federated_dm",ephemeral_typing:true}];
  const assets = new Map([["/",["index.html","text/html"]], ["/static/app.js",["app.js","text/javascript"]],
    ["/static/i18n.js",["i18n.js","text/javascript"]], ["/static/style.css",["style.css","text/css"]]]);
  const server = http.createServer(async (req,res) => {
    const url = new URL(req.url,"http://localhost");
    const asset = assets.get(url.pathname);
    if (asset) {res.writeHead(200,{"Content-Type":asset[1]});return res.end(await fs.readFile(path.join(__dirname,"..","web",asset[0])))}
    if (req.headers.authorization !== "Bearer fixture-token") return res.writeHead(401).end();
    requests.push({path:url.pathname,method:req.method});
    const json = (data) => {res.writeHead(200,{"Content-Type":"application/json"});res.end(JSON.stringify(data))};
    if (url.pathname === "/v1/me") return json({agent:{id:"person",name:"person",kind:"human"},rooms:rooms.map(r=>r.id)});
    if (url.pathname === "/v1/rooms") return json({rooms});
    if (url.pathname === "/v1/agents") return json({agents:[]});
    if (url.pathname.endsWith("/events")) {
      res.writeHead(200,{"Content-Type":"text/event-stream"});res.write("event: ready\ndata: {}\n\n");
      if(url.pathname==="/v1/rooms/general/events" && latestRemote && latestRemote.expires>Date.now())res.write("event: typing\ndata: "+JSON.stringify({agents:[],remote_agents:[{...latestRemote,ttl_ms:latestRemote.expires-Date.now()}]})+"\n\n");
      streams.set(url.pathname,res);res.on("close",()=>{if(streams.get(url.pathname)===res)streams.delete(url.pathname)});return;
    }
    if (url.pathname.endsWith("/messages")) return json({messages:[],has_more:false});
    if (url.pathname.endsWith("/receipts")) return json({receipts:[]});
    if (url.pathname.endsWith("/typing") || url.pathname === "/v1/ack") return res.writeHead(204).end();
    res.writeHead(404).end();
  });
  await new Promise(resolve=>server.listen(0,"127.0.0.1",resolve));
  let browser;
  try {
    browser = await chromium.launch({headless:true,...(process.env.BROWSER_EXECUTABLE?{executablePath:process.env.BROWSER_EXECUTABLE}:{})});
    const page = await browser.newPage({locale:"en-US"}), errors=[];page.on("pageerror",e=>errors.push(e.message));
    await page.goto(`http://127.0.0.1:${server.address().port}/`);
    await page.evaluate(()=>window.AgentChatI18n.setLang("en"));
    await page.locator("#token").fill("fixture-token");await page.locator('#login-form button[type="submit"]').click();
    await page.waitForFunction(()=>document.getElementById("room-title").textContent.includes("general"));
    const emit = (name,ttl=5000) => {
      const res=streams.get("/v1/rooms/general/events");assert.ok(res,"SSE connected");
      latestRemote={node_id:"peer",agent_id:"remote-person",name,kind:"human",ttl_ms:ttl,revision:1,expires:Date.now()+ttl};
      res.write("event: typing\ndata: "+JSON.stringify({agents:[],remote_agents:[latestRemote]})+"\n\n");
    };
    for(let n=0;!streams.has("/v1/rooms/general/events")&&n<200;n++)await new Promise(resolve=>setTimeout(resolve,10));
    // Matching local display name is still a distinct remote principal.
    emit("person",1200);await page.waitForFunction(()=>document.getElementById("typing").textContent.includes("person@peer"));
    await page.locator("#lang-toggle").click();
    await page.waitForFunction(()=>document.getElementById("typing").textContent.includes("远端"));
    await page.waitForFunction(()=>document.getElementById("typing").classList.contains("hidden"),{},{timeout:2000});
    emit('<img src=x onerror="window.injected=true">');
    await page.waitForFunction(()=>document.getElementById("typing").textContent.includes("<img"));
    assert.equal(await page.locator("#typing img").count(),0);assert.equal(await page.evaluate(()=>!!window.injected),false);
    streams.get("/v1/rooms/general/events").end();
    await page.waitForFunction(()=>document.getElementById("typing").classList.contains("hidden"),{},{timeout:1500});
    await page.locator("#dm-list button").click();await page.locator("#enable-writing").click();
    const typingResponse=page.waitForResponse(r=>r.url().endsWith("/v1/rooms/fdm_test/typing") && r.status()===204);
    await page.locator("#message-text").fill("new local input");
    await typingResponse;
    assert.equal(requests.some(r=>r.path==="/v1/rooms/fdm_test/receipts"),false);
    assert.equal(errors.length,0,errors.join("\n"));console.log("federated typing browser regression passed");
  } finally {if(browser)await browser.close();for(const res of streams.values())res.end();await new Promise(resolve=>server.close(resolve))}
})().catch(err=>{console.error(err);process.exitCode=1});
