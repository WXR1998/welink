# WeLink 企业微信内部群机器人

通过企业微信“智能机器人 API 模式”的长连接，在企业内部群接收 `@机器人` 文本消息，并调用 WeLink 的跨联系人 AI 问答接口。

## 范围

- 仅企业内部群。
- 仅文本消息。
- 外部群、客户联系、传统群 Webhook、模板卡片与主动定时推送不支持。

## 管理后台配置

1. 创建企业微信智能机器人。
2. 开启“API 模式”，选择“长连接”。
3. 复制 BotID 与长连接 Secret。
4. 将机器人加入企业内部群。

## 启动

```sh
cd wecom-bot
export WECOM_BOT_ID="bot_id"
export WECOM_BOT_SECRET="long_connection_secret"
export WELINK_BASE_URL="http://127.0.0.1:8080"
export ALLOWED_USERS="zhangsan,lisi"
export BOT_NAME="知识助手"

GOTMPDIR=/volume4/homes/wangxuanrun/.local/tmp \
GOFLAGS=-mod=mod \
/volume4/homes/wangxuanrun/.local/go/bin/go run .
```

`ALLOWED_USERS` 使用企业微信内部成员 ID。留空会允许所有可触发该机器人的企业内部成员，生产环境不建议留空。

## Docker Compose

```sh
WECOM_BOT_ID="bot_id" \
WECOM_BOT_SECRET="long_connection_secret" \
ALLOWED_USERS="zhangsan,lisi" \
docker compose --profile wecom up -d wecom-bot
```

企业微信要求同一机器人同时只保持一条有效长连接，因此 Compose 服务必须维持单副本。
