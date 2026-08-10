# 企业微信内部群机器人调研与落地范围

## 结论

企业微信的“智能机器人”API 模式可以承担企业内部群里的 AI 问答机器人：成员在群内 @ 机器人后，企业微信通过回调或长连接把消息交给服务端；服务端可以回复 Markdown、流式消息或模板卡片。

本仓库采用长连接模式。机器人服务主动连接企业微信 WebSocket，不需要暴露公网回调地址。该模式适合部署在 NAS 上的 WeLink。

外部群不在本次适配范围内。智能机器人消息回调的发送者模型为企业主体下的 `userid`，文档没有给出外部联系人/微信用户的对话式机器人接入契约。因此不能把本实现用于含外部联系人的客户群；这类场景只能使用传统群 Webhook 做通知，或改为群内入口跳转到小程序、H5、微信客服。

## 能力对照

| 需求 | 企业微信智能机器人 | 本次实现 |
| --- | --- | --- |
| 群内 @ 机器人提问 | 支持 `aibot_msg_callback`，群聊 `chattype=group` | 支持 |
| 接收消息 | 长连接 WebSocket `wss://openws.work.weixin.qq.com` | 支持 |
| AI 流式回答 | `aibot_respond_msg`，同一 `req_id` + `stream.id` 刷新 | 支持 |
| 会话隔离、白名单、超时 | 平台无业务实现 | 复用 WeLink 飞书网关的设计 |
| 卡片按钮/投票/表单 | 模板卡片事件回调 | 不实现 |
| 主动定时消息 | 长连接 `aibot_send_msg` | 不实现 |
| 外部群机器人对话 | 官方智能机器人文档未提供外部联系人消息契约 | 不支持 |

## 企业管理员操作

1. 在企业微信管理后台创建智能机器人。
2. 在机器人配置中开启“API 模式”，选择“长连接”。
3. 获取 `BotID` 和长连接专用 `Secret`。
4. 将机器人加入企业内部群；成员在群中 @ 机器人触发提问。
5. 配置 WeLink 网关的 `WECOM_BOT_ID`、`WECOM_BOT_SECRET`、`WELINK_BASE_URL`，并建议配置 `ALLOWED_USERS`。

官方 API 文档描述的是管理员配置机器人、获取 BotID/Secret 并建立连接的过程，没有将企业认证或腾讯代码审核列为智能机器人 API 的通用前置条件。企业自身的管理员权限、可用范围和安全策略仍须由实际租户确认。

## 实现架构

```text
企业内部成员在群内 @ 智能机器人
        |
        v
企业微信 WebSocket 长连接
        |
        v
wecom-bot
  - 订阅身份校验
  - 仅接收 group + text 回调
  - ALLOWED_USERS 校验
  - 每群每人独立会话
        |
        v
WeLink /api/ai/memory-search -> /api/ai/analyze
        |
        v
aibot_respond_msg 流式回复
```

## 协议要点

- 连接地址：`wss://openws.work.weixin.qq.com`
- 建连后发送 `aibot_subscribe`，body 包含 `bot_id` 与 `secret`。
- 企业微信推送 `aibot_msg_callback`。本实现只处理 `chattype=group` 且 `msgtype=text` 的帧。
- 所有对同一消息的流式回复都透传该回调的 `headers.req_id`；首次回复生成 `stream.id`，后续刷新使用相同 ID，最后以 `finish=true` 结束。
- 单个机器人同时只能保持一个有效长连接；部署侧应使用单副本，或自行做主备切换。

## 官方文档

- [智能机器人概述](https://developer.work.weixin.qq.com/document/path/101039)
- [接收消息](https://developer.work.weixin.qq.com/document/path/100719)
- [接收事件](https://developer.work.weixin.qq.com/document/path/101027)
- [被动回复消息](https://developer.work.weixin.qq.com/document/path/101031)
- [模板卡片类型](https://developer.work.weixin.qq.com/document/path/101032)
- [主动回复消息](https://developer.work.weixin.qq.com/document/path/101138)
- [智能机器人长连接](https://developer.work.weixin.qq.com/document/path/101463)
- [传统群机器人消息推送 Webhook](https://developer.work.weixin.qq.com/document/path/99110)

