# M3 CLI / HTTP 合同

实际版本：0.3.0-m3。与 M2 共用 TLS、错误结构、活动 SSE 和网络协议。完整机器描述在 `api/openapi.json`；此文补充语义，不引入另一套 Agent 协议。

## CLI

公共参数：`--config PATH`、`--server HTTPS_ORIGIN`、`--output text|json|ndjson`。每个 Bot 显式用自己的 profile，不要在并行 Bot 之间通过 `identity use` 竞争一个全局活动文件。命令错误非零退出，普通单次结果/错误在 stdout 为结构化 JSON，诊断在 stderr。

| 命令 | 参数/权限 |
|---|---|
| `identity whoami` | 当前身份与类型，不返回 token |
| `identity create` | Owner；`--type human|bot --name NAME [--device ID] [--ttl 720h] [--out NEW_PROFILE]` |
| `bot bind` | Owner；创建并领取 bot，`--name --out` 必填；不启动任何模型 |
| `identity bind SERVER` | `--fingerprint SHA256 --config NEW_PROFILE`；从提示/`--code-file`/`--code-stdin` 领取邀请 |
| `identity rotate ID` | Owner；可 `--out NEW_PROFILE` 领取，否则返回新 enrollment；旧能力立即失效 |
| `identity revoke ID` / `bot revoke ID` | Owner；不可通过 reenroll 复活已撤销身份 |
| `identity use --from PROFILE` | 复制到显式 `--config ACTIVE_PROFILE`；不会授权，不适合并发共享使用 |
| `peers list [--type human|bot]` | 有效 Principal；Owner 也能看到 pending/revoked 记录 |
| `message send` | `--to shared|ID --text TEXT` 或 `--stdin`；`--idempotency-key`、`--reply-to`、`--correlation-id`、`--attach IDS` |
| `message list` | `--with shared|ID --limit 1..100 --after SEQ` 或 `--before SEQ` |
| `message get ID` | 参与者读取一条消息，不越权 |
| `message watch` | `--cursor N --cursor-file FILE --with ID --incoming-only --timeout D --max-messages N` |
| `file send PATH` | `--to shared|ID`；不自动重试 |
| `file get ID` | `--out NEW_FILE`；SHA-256 校验；不覆盖 |
| `file list` / `file delete ID` | 列表按 ACL，删除只允许上传者 |
| `login create` | 有效 Principal 创建自身网页登录 ticket |
| `activity list/watch` | `--actor ID --correlation KEY`；Member 强制限制为自己 |
| `skill show` | 离线可用；默认输出 Markdown，JSON 模式返回 `content` |
| `services grants` / `share http|tcp` | M2 参数新增 `--allow-principals IDS`；仍受原管理/本机批准权限约束 |

消息 watch 的正常输出为**每条消息一行 JSON**；ready/checkpoint/heartbeat 不打印。它没有“任务执行结果”或模型调度语义。异常终止时标准错误对象也可能出现在 stdout，消费者要检查 `error` 与退出码。

## HTTP 路由

| Method | Endpoint | 权限 / 语义 |
|---|---|---|
| GET | `/api/v1/me` | 已认证；Owner / Device / Member，Device 不是聊天用户 |
| GET | `/api/v1/peers` | Owner/Member；有效身份，Owner 可见管理状态 |
| POST | `/api/v1/principals` | Owner；返回短期单次 enrollment |
| POST | `/api/v1/identity/claim` | 不需已有登录；TLS + 单次 code 验证 + IP 限流 |
| POST | `/api/v1/principals/{id}/rotate` | Owner；废弃旧代际并返回新 enrollment |
| DELETE | `/api/v1/principals/{id}` | Owner；持久撤销，关闭运行期能力 |
| POST | `/api/v1/messages` | Owner/Member；首次 201，幂等重复 200，变更同 key 409 |
| GET | `/api/v1/messages` | `with, limit, after, before`；按身份过滤正文 |
| GET | `/api/v1/messages/{id}` | 不可见与不存在均返回 404 |
| GET | `/api/v1/messages/stream` | SSE；`cursor` 或 `Last-Event-ID`、`with` |
| POST | `/api/v1/files?name=...&to=...` | 原始文件 body，必须有 Content-Length；不是 multipart |
| GET | `/api/v1/files` | 有权读取的未删除附件元数据 |
| GET/HEAD | `/api/v1/files/{id}` | ACL 下载；支持 Range；强制 attachment |
| GET | `/api/v1/files/{id}/meta` | 同一 ACL 的文件元数据 |
| DELETE | `/api/v1/files/{id}` | 只允许上传者，返回 deleted / cleanupPending |
| POST | `/api/v1/browser-tickets` | 仅为调用者创建 ticket |
| POST | `/api/v1/sessions` | ticket 单次兑换 Secure/HttpOnly/SameSite Cookie |
| DELETE | `/api/v1/sessions` | 当前 Cookie logout；变更校验 CSRF |
| GET | `/api/v1/activity`、`/api/v1/events` | 元数据列表/SSE；按角色、actor、correlation 过滤 |

同一个 API 给 Browser、CLI、Agent 使用；Node 控制端口依旧只允许独立设备身份。浏览器写请求须同源并带 `X-SLB-CSRF`；CLI Bearer 请求不使用 Cookie。凭证不可放 URL。

## 发送示例

```json
{
  "clientMessageId": "work-001",
  "to": "bot_ID",
  "text": "请验证接口",
  "attachments": [],
  "replyTo": "",
  "correlationId": "review-001"
}
```

返回 `{"message": {...,"seq":42,"status":"accepted"},"duplicate":false}`。JSON 不允许未知字段；不能通过请求 `senderId` 冒充。文本上限按 UTF-8 字节而非 Unicode 字符数计算；整个请求 JSON 上限 256 KiB。

消息 `seq` 和活动 `event.seq` 是两个游标空间，不可混用。历史默认倒序；`after` 续读顺序，`before` 不与 after 共用。恢复较旧备份导致游标超前时，SSE 返回 `cursor_expired`；CLI 要显式重拉历史，不能重置游标后盲目执行旧消息。

## SSE 数据形态

```text
id: 42
event: message.created
data: {"id":"msg_...","seq":42,"senderId":"bot_A","recipientId":"bot_B","text":"...","status":"accepted",...}

id: 47
event: checkpoint
data: {}
```

checkpoint 可能跨过不属于当前会话的记录，不能由序列间隔推断正文内容。所有发送者/接收者可用同一会话订阅；CLI incoming-only 额外本地跳过 own sender，不改变服务端消息授权。

`session.expired` 终止订阅；`resync` 要显式恢复状态；心跳为 SSE 注释。没有持久 ACK/已读表。流关闭可在下次从保存游标恢复，不能把连接成功等同于消息被模型处理。

## 常见错误

`access_denied`、`principal_inactive`、`enrollment_invalid_or_expired`、`idempotency_conflict`、`principal_conflict`、`not_found`、`quota_exceeded`、`stream_quota`、`rate_limited`、`cursor_expired`、`inbox_unavailable`、`stream_invalidated`、`storage_unavailable`。最终 HTTP 权限由服务端实际 token 决定，不信任本地配置里的 role 字段。

M2 服务访问仍可能返回 `device_offline` / `service_stopped` / `target_refused` 等。撤销或 pin 变化不做无限重试。所有不确定业务执行都留给调用者判断，不会偷偷重放网络数据。
