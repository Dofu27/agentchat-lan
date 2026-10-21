# Codex handoff message

Copy the block below into Codex on agent-host.

```text
你是 Codex，接手 AgentChat LAN 的复杂核心实现。这个项目是局域网内不同 AI
Agent 的聊天与文件中转站，最终要开源到 GitHub。DSH 已搭好 M0 脚手架、协议、
mock hub 和 CLI 文本链路；简单代码由 DSH 继续负责，复杂核心由你负责。

仓库已复制到：
C:\work\agentchat-lan

先按顺序读：
1. docs/CODEX_TASKS.md
2. docs/CONTRACT.md
3. docs/DECISIONS.md
4. docs/STATUS.md

你的 M1 任务（优先做 C1/C2，不要先做 UI）：
- internal/store：SQLite schema、迁移、事务、per-room seq、ack cursor
- internal/hub：生产 HTTP 服务、join/invite、token 哈希、房间、消息、inbox、
  ack、幂等、长轮询、限流、审计、优雅退出
- 用标准库 + CGO-free SQLite（modernc.org/sqlite）；不要引入重框架
- 写测试，跑 go test -race ./... 和 go vet ./...
- 不改 internal/protocol 的 JSON 字段/端点/状态码；确需改动先改
  docs/CONTRACT.md 并在 docs/STATUS.md 记录
- 不碰 internal/cli、internal/mockhub、internal/config、docs/README 等 DSH 负责区
- 不提交 token、局域网 IP、个人路径
- 默认只监听 127.0.0.1，局域网暴露必须显式传 --listen

DSH 已经验证：
- mock hub：join/send/inbox/ack/长轮询/幂等 可用
- Windows 和 Linux 单文件 CLI 可编译运行
- 现有测试全部通过

交付方式：
- 把改动直接留在工作区，并在 docs/STATUS.md 写 done/next/blockers
- 如果能访问 GitHub，用 feat/hub-core 分支和 PR；不能就保留 patch/提交
- 先做能跑通的最小闭环：两个 agent 通过真实 hub 互发消息、重启不丢、离线补发
- 文件服务和 MCP/Web UI 放到 M2，不要提前展开

完成后回报：改了什么、怎么验证、还有哪些契约未决、下一步建议。
```

## Screenshot handoff (quota restored)

The exact post-quota screenshot steps are in
[SCREENSHOTS.md](SCREENSHOTS.md#codex-handoff-after-quota-returns).

Short version: open AgentChat Console, capture Overview in English and 中文,
then Chat and Admin, and send the six PNGs back; do not expose tokens or private
addresses.
