# 飞书群 AI 问答机器人：开发进度

> 本文档记录「把 WeLink 的 AI 问答能力接入飞书群」这一功能的当前开发进度，随时代码状态更新。文中不包含任何真实飞书凭证。

## 一句话现状

跨联系人 AI 问答网关的核心链路已搭好并可编译运行，飞书长连接已通过真实自检验证；"提问必须指定实体"的收尾已完成，后端在 `resolve_entities` 阶段输出命中结果，网关据此在慢检索前精确中止。

## 目标范围

- 在国内、不备案、不把 WeLink 后端暴露公网的前提下，让飞书群成员能用自然语言查询 WeLink 的跨联系人 AI 问答。
- 每个用户独立上下文：只响应 @bot 消息；回答中拒绝同人并发提问；2 小时无提问自动新开会话；上下文过长自动压缩。
- 提问必须能解析到至少一个实体（联系人/群名）才进入检索，否则直接报错，避免全库慢扫。

## 分支与提交

- 工作分支：`codex/feishu-ai-bot`
- 已推送远端：`github.com:WXR1998/welink.git`（origin）
- 已提交记录（从新到旧）：

| 提交 | 说明 |
|------|------|
| `748f174` | 会话历史持久化到 JSON |
| `8005b99` | 修复压缩并发竞态 + 部署文档 |
| `94f7112` | 切换到跨联系人问答 + 会话隔离 |
| `7fe84ce` | 增加卡片流式模式 |
| `da7d7a6` | 新增飞书群网关初版 |
| `dc0936c` | 飞书 AI 问答机器人调研文档 |

## 本次完成改动

以下改动完成了"提问必须指定实体"的收尾，包括后端输出命中结果和网关精确中止：

- `backend/memory_search.go`：在 `resolve_entities` 阶段，实体解析完成后发送第二条进度 `"实体解析结果: 已命中"` 或 `"实体解析结果: 未命中"`，让网关在慢检索前精确判断。
- `feishu-bot/ai.go`：`memorySearch` 处理两条 `resolve_entities` 进度——第一条解析实体名（无实体则 `errMissingEntity` 中止），第二条检查命中结果（未命中则 `errEntityNotFound` 中止）。
- `feishu-bot/bot.go`：会话新增 `entities` 字段，`answer` 在检索前检查是否已有实体；无实体且上下文无实体时返回"请指定联系人/群名"；指定了实体但未命中时返回"未找到你指定的联系人/群名"。
- `feishu-bot/check.go`：新增 `--smoke` 冒烟命令，并新增以"邓凯文"为测试实体的 `runSmokeEntity`/`--smoke-entity`。
- `feishu-bot/store.go` / `store_test.go`：会话持久化补充 `entities` 字段。
- `feishu-bot/ai_test.go`：新增 `TestMemorySearch_AbortsOnEntityNotFound` 和 `TestMemorySearch_ProceedsOnEntityHit` 测试。
- `.gitignore`：新增 `feishu-bot/welink-feishu-bot` 构建产物忽略。
- `docs/feishu-ai-bot.md`、`feishu-bot/README.md`：补充实体约束与性能说明。

## 已实现能力

- 飞书长连接（WebSocket）接收消息，不需要公网 IP/域名。
- `--check` 三段自检：飞书凭证、WeLink 后端、长连接就绪。
- `--smoke`：不依赖真实飞书消息，直接调用跨联系人问答做链路冒烟。
- 跨联系人问答链路：`memory-search` → 构造检索上下文 → `analyze` 生成回答。
- 每人独立上下文（单聊按 `user_id`，群聊按 `chat_id + sender_id`）。
- 只响应 @bot 消息（单聊始终响应）。
- 回答中冷却：同人在处理期间再提问，返回错误提示。
- 2 小时无提问自动清空历史、新开会话。
- 上下文过长时调用 `/api/ai/complete` 压缩成摘要，写回带版本号保护。
- 会话历史可选持久化到 JSON（`SESSION_STORE_PATH`）。
- 回复载体：Markdown 富文本（默认 `md`）或卡片（`card`）。
- 实体门控：无实体直接拒绝（`errMissingEntity`）；指定了实体但后端未解析到 contact_key 时也快速拒绝（`errEntityNotFound`），避免全库慢扫。

## 实测发现

- 用提供的飞书应用身份运行 `--check`：凭证有效、WeLink `:8080` 可达、长连接就绪，全部通过。
- `--smoke "谁最近和我聊得最多？"`（无实体）：`resolve_entities` 阶段即返回"未指定实体"错误，未进入全库扫描。说明实体门控的基础逻辑已生效。
- `--smoke-entity`（"我和邓凯文最近聊了什么？"）：在旧版本里会触发全库扫描（约 506 个 key）并最终超时/失败。**修复后**：后端在 `resolve_entities` 阶段发送 `"实体解析结果: 未命中"`，网关收到后立即返回 `errEntityNotFound`，不再进入慢检索。
- 网关侧已加 `memorySearchTimeout = 3m`，超时会返回可读错误，不会无限挂起；但会占用会话直到超时。

## 结论与待办

- 一条正向结论：网关侧的"无实体直接拒绝"做得通，且能避免纯开放问题进入全库扫描。
- 本次修复了上游问题：后端 `memory-search` 在 `resolve_entities` 阶段输出"是否命中 contact_key"的信息，网关据此在慢检索前精确中止。
- 下一步建议：
  1. 确认"邓凯文"对应的真实联系人或群在 WeLink 数据里用哪个名字/备注，再验证 `--smoke-entity` 能真正收敛检索范围。
  2. 实体门控收尾完成后，跑通 `--smoke-entity`（单实体收敛），再做真实群聊消息联调。
  3. 清理本地构建产物 `feishu-bot/welink-feishu-bot`，或加入 `.gitignore`。

## 验证命令

```bash
# 自检（飞书凭证 / WeLink / 长连接）
cd feishu-bot
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go run . --check

# 冒烟（带实体，示例：邓凯文）
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go run . --smoke-entity

# 单元测试
GOTMPDIR=/var/services/homes/wangxuanrun/.local/tmp GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go test ./...

# 编译 / vet
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go build -o /dev/null .
GOFLAGS=-mod=mod /volume4/homes/wangxuanrun/.local/go/bin/go vet ./...
```
