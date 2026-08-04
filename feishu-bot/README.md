# WeLink 飞书群 AI 问答网关

把 WeLink 的 **跨联系人 AI 问答**接入飞书群/单聊。机器人通过飞书长连接（WebSocket）**主动出站**接收消息，不需要公网 IP、不需要备案；问题在 NAS 本机调用 WeLink 跨联系人接口，聊天数据不经过公网中转。

## 能力

- 群聊 @ 机器人提问、单聊直接提问；**只有 @ 机器人的消息会被当作提问**。
- 调用 WeLink 跨联系人问答：`memory-search`（记忆检索）→ `analyze`（生成回答），复用前端「跨联系人问答」的 Agent 链路。
- 每人独立上下文：单聊按 `user_id` 区分，群聊按 `chat_id + sender_id` 区分，各自维护独立的问答历史。
- **回答中冷却**：AI 正在回答某人的问题时，同人再次提问会被拒绝并返回提示。
- **2 小时过期**：某人超过 2 小时没有新提问，自动丢弃旧上下文、新起一个会话。
- **上下文压缩**：历史达到一定条数/长度后，自动调用后端把旧对话压成摘要，保留最近一问一答，供后续多轮追问。
- 流式回传：先发“处理中”，再分阶段显示检索/生成进度，最终以 Markdown 回传完整回答（`md` 默认，`card` 可选）。

## 前置条件

1. 已开通飞书企业，并在开放平台创建**企业自建应用**：
   - 开启「机器人」能力
   - 申请权限：`im:message.p2p_msg:readonly`、`im:message:send_as_bot`、`im:message.group_at_msg:readonly`
   - 事件订阅设置为**长连接**，订阅 `im.message.receive_v1`
2. WeLink 后端在 NAS 上运行（默认 `http://127.0.0.1:8080`），且已配置好记忆提炼/跨联系人问答所需索引。
3. （可选）配置 `ALLOWED_USERS` 白名单，避免任意远程用户读取全部聊天记录。

## 运行

```bash
cd feishu-bot
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your_secret"
export WELINK_BASE_URL="http://127.0.0.1:8080"
export ALLOWED_USERS="ou_xxx"   # 逗号分隔；不设=全部放行（慎用）

# 自检（验证飞书凭证 / WeLink / 长连接）
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go run . --check

# 启动
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go run .
```

## 环境变量

| 变量 | 说明 | 默认 |
|------|------|------|
| `FEISHU_APP_ID` / `FEISHU_APP_SECRET` | 飞书自建应用凭证 | 必填 |
| `FEISHU_CONFIG` | 指向 JSON 配置文件（可选，覆盖 env） | - |
| `WELINK_BASE_URL` | WeLink 后端地址 | `http://127.0.0.1:8080` |
| `WELINK_TOKEN` | 配对 token。Docker 内非回环访问时需带 | 空（回环自动放行） |
| `DEFAULT_PROFILE_ID` | 使用的 LLM Profile ID | 空（用默认） |
| `STREAM_MODE` | 流式回复载体：`md`（默认）或 `card` | `md` |
| `ALLOWED_USERS` | 允许的飞书用户，逗号分隔 | 空=不限制 |

## 会话机制

| 规则 | 实现 |
|------|------|
| 每人独立上下文 | `sessionKey = group:chatID:senderID`（群聊）或 `p2p:userID`（单聊） |
| 只 @bot 才提问 | 群聊只有 `MentionedBot` 才处理；单聊始终响应 |
| 回答中冷却 | 会话 `busy` 锁：处理期间同人再次提问返回错误 |
| 2 小时过期 | 超过 `2h` 无提问自动清空历史并新开会话 |
| 上下文压缩 | 达到条数/长度阈值后调用 `/api/ai/complete` 压缩成摘要，保留最近一问一答 |

## 说明与限制

- 跨联系人问答依赖 WeLink 端已构建好记忆事实（`mem_facts`）和向量索引；若未建索引，检索可能返回空。
- `memory-search` 步骤在网关侧打印进度日志；飞书消息以最终答案为主，不逐条回帖中间态。
- 会话历史保存在内存中，重启网关后丢失；当前未做持久化。
- 飞书 3 秒约束：收到消息立即异步启动处理，避免超时重推。
- 长文本由 SDK 按块拆分并以 Markdown/富文本回传，客户端 7.20+ 支持流式上屏。

## 开发

```bash
cd feishu-bot
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /tmp/feishu-bot .
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go vet ./...
GOTMPDIR=/var/services/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...
```

> 飞书长连接、卡片流式能力与客户端版本会随飞书平台演进，实施前以开放平台当前文档为准。
