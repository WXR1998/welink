# WeLink 飞书群 AI 问答网关

把 WeLink 的 AI 问答能力接入飞书群/单聊。机器人通过飞书长连接（WebSocket）**主动出站**接收消息，不需要公网 IP、不需要备案；问题在 NAS 本机调用 WeLink 现有接口，然后再异步回传，聊天数据不经过公网中转。

## 能力

- 群聊 @ 机器人提问、单聊直接提问。
- 长连接接收消息，不暴露 WeLink 入站端口。
- 调用现有 `POST /api/ai/rag`，复用 WeLink 的混合检索（向量 + FTS + 记忆事实）。
- 流式回传：先发“处理中”，再分阶段更新（分析中 / 检索中 / 整理中），最终以 Markdown 回传完整回答。
- 会话隔离：单聊按 `user_id` 区分，群聊按 `chat_id + sender_id` 区分，避免多人共享上下文。
- 用户白名单 + 联系人 key 白名单（可选）。

## 前置条件

1. 已开通飞书企业，并在开放平台创建**企业自建应用**：
   - 开启「机器人」能力
   - 申请权限：`im:message.p2p_msg:readonly`、`im:message:send_as_bot`、`im:message.group_at_msg:readonly`
   - 事件订阅设置为**长连接**，订阅 `im.message.receive_v1`
2. WeLink 后端在 NAS 上运行（默认 `http://127.0.0.1:8080`）。
3. 已配置 LLM / Embedding / RAG 索引（在 WeLink 前端设置里完成）。

## 运行

```bash
cd feishu-bot
export FEISHU_APP_ID="cli_xxx"
export FEISHU_APP_SECRET="your_secret"
export WELINK_BASE_URL="http://127.0.0.1:8080"
export DEFAULT_AI_KEY="contact:alice"
export ALLOWED_USERS="ou_xxx"   # 逗号分隔；不设=全部放行（慎用）

# 或用配置文件
# export FEISHU_CONFIG=/path/to/config.example.json

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
| `DEFAULT_AI_KEY` | 检索范围 key，如 `contact:alice` | 必填 |
| `ALLOWED_KEYS` | 允许的 key 白名单，逗号分隔 | 空=不限制 |
| `ALLOWED_USERS` | 允许的飞书用户，逗号分隔 | 空=不限制 |

## 说明与限制

- `DEFAULT_AI_KEY` 必填；`ALLOWED_KEYS` 是额外的请求级白名单（配置后仅放行命中项）。
- 会话历史目前只保留最近几轮**问题**（不做持久化），只为多轮追问提供轻量上下文。
- 飞书 3 秒约束：收到消息立即异步启动处理，避免超时重推。
- 长文本由 SDK 按块拆分并以 Markdown/富文本回传，客户端 7.20+ 支持流式上屏。

## 开发

```bash
cd feishu-bot
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /tmp/feishu-bot .
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go vet ./...
```

> 飞书长连接、卡片流式能力与客户端版本会随飞书平台演进，实施前以开放平台当前文档为准。
