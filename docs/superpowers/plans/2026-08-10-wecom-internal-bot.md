# Enterprise WeChat Internal Bot Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a standalone Enterprise WeChat smart-bot gateway for internal group AI Q&A.

**Architecture:** Create `wecom-bot` as an isolated Go module. It owns the Enterprise WeChat WebSocket protocol and translates only internal group text callbacks into the existing WeLink cross-contact Q&A HTTP flow. It maintains per-group-per-user sessions and streams the answer through the callback's `req_id`.

**Tech Stack:** Go 1.24, `github.com/gorilla/websocket`, WeLink HTTP/SSE APIs, Docker Compose.

---

### Task 1: Define the WebSocket protocol boundary

**Files:**
- Create: `wecom-bot/protocol.go`
- Create: `wecom-bot/protocol_test.go`

- [ ] **Step 1: Write failing protocol tests**

```go
func TestDecodeIncomingGroupTextAcceptsInternalGroupText(t *testing.T) {
    msg, ok, err := decodeIncomingGroupText([]byte(`{
      "cmd":"aibot_msg_callback",
      "headers":{"req_id":"req_1"},
      "body":{"msgid":"msg_1","chatid":"chat_1","chattype":"group",
      "from":{"userid":"zhangsan"},"msgtype":"text","text":{"content":"@机器人 你好"}}
    }`))
    if err != nil || !ok || msg.ChatID != "chat_1" || msg.UserID != "zhangsan" {
        t.Fatalf("unexpected result: %#v, ok=%v, err=%v", msg, ok, err)
    }
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: FAIL because `decodeIncomingGroupText` is undefined.

- [ ] **Step 3: Implement minimal frame decoding**

Create typed frame structs for `cmd`, `headers.req_id`, and `aibot_msg_callback` text body. Return `ok=false` for any non-group or non-text callback.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: PASS.

### Task 2: Add configuration and streaming reply encoding

**Files:**
- Create: `wecom-bot/config.go`
- Create: `wecom-bot/config_test.go`
- Modify: `wecom-bot/protocol.go`
- Modify: `wecom-bot/protocol_test.go`

- [ ] **Step 1: Write failing tests**

Test missing `WECOM_BOT_ID`/`WECOM_BOT_SECRET` validation and verify that `newStreamReply` preserves the callback request ID, stream ID, content, and `finish` flag.

- [ ] **Step 2: Run tests to verify failure**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: FAIL because the config loader and stream reply constructor do not exist.

- [ ] **Step 3: Implement configuration and reply constructors**

Read credentials and existing WeLink gateway settings from environment. Define a frame constructor for `aibot_subscribe` and `aibot_respond_msg`.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: PASS.

### Task 3: Implement the internal-group gateway

**Files:**
- Create: `wecom-bot/bot.go`
- Create: `wecom-bot/ai.go`
- Create: `wecom-bot/store.go`
- Create: `wecom-bot/main.go`
- Create: `wecom-bot/bot_test.go`

- [ ] **Step 1: Write failing behavior tests**

Test that only an allowed group user gets a session key `wecom:group:<chat>:<user>`, while an unlisted user receives a permission reply and never reaches the answer pipeline.

- [ ] **Step 2: Run tests to verify failure**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: FAIL because the gateway dispatcher does not exist.

- [ ] **Step 3: Implement the minimal gateway**

Serialize WebSocket writes, subscribe with BotID/Secret, deduplicate callback message IDs, reject non-group/non-text callbacks, maintain a busy lock per session, call WeLink `memory-search` and `analyze`, and stream the answer with `aibot_respond_msg`.

- [ ] **Step 4: Run tests to verify pass**

Run: `cd wecom-bot && GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...`

Expected: PASS.

### Task 4: Package and document the service

**Files:**
- Create: `wecom-bot/Dockerfile`
- Create: `wecom-bot/README.md`
- Create: `wecom-bot/config.example.json`
- Modify: `docker-compose.yml`
- Modify: `docker-compose.dev.yml`
- Modify: `Makefile`

- [ ] **Step 1: Add Compose and build targets**

Add an opt-in `wecom` service profile, its own session volume, and a `build-wecom-bot` target. Keep the existing Feishu service unchanged.

- [ ] **Step 2: Add operator documentation**

Document Enterprise WeChat admin configuration, credentials, internal-group-only restriction, environment variables, container start command, and checks.

- [ ] **Step 3: Verify the integration**

Run:

```sh
cd frontend
node ./node_modules/typescript/bin/tsc --noEmit
cd ../backend
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /dev/null .
cd ../wecom-bot
GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /dev/null .
cd ..
docker compose -f docker-compose.yml config --quiet
docker compose -f docker-compose.dev.yml config --quiet
```

Expected: all commands exit 0.

- [ ] **Step 4: Commit**

```sh
git add docs/enterprise-wechat-bot.md docs/superpowers/plans/2026-08-10-wecom-internal-bot.md wecom-bot Dockerfile Makefile docker-compose.yml docker-compose.dev.yml
git commit -m "feat(wecom): add internal group bot gateway"
```

