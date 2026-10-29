# M1 CLI 与连接契约

程序：`seven-air-link` / Windows `seven-air-link.exe`。

公共参数 `--config FILE`、`--server https://HOST:PORT`、`--output text|json|ndjson` 可在子命令前后使用。普通子命令参数应在位置参数后使用。配置中的 `server` 可以由 `--server` 覆盖，但证书指纹不变，禁止静默改变信任目标。

成功返回 0，失败返回非零（当前统一 1）。JSON 模式的错误位于 stdout；解析错误等诊断位于 stderr，不污染 JSON。Node 运行日志为 stderr NDJSON。默认文本模式部分命令仍输出便于阅读的缩进 JSON；`devices list` 为表格式文本。

| 命令 | 实际语义 |
|---|---|
| `init --data DIR [--port 7777] [--tls-host a,b]` | 幂等初始化；不覆盖旧 Python 数据、不静默旋转凭证 |
| `serve --data DIR [--listen HOST:PORT] [--port PORT]` | HTTPS 服务；启动时自动 init |
| `pairing create --ttl 5m [--out FILE]` | Owner 签发单次邀请；写文件时不同时打印密钥 |
| `join https://HOST:PORT --fingerprint SHA256 --name NAME --config FILE` | 用输入的邀请配对；不会自动运行 Node |
| `node start --config FILE` | 使用设备配置前台运行控制长连接 |
| `devices list` | Owner 看全体；Device 只看自己 |
| `devices rename ID --name NAME` | Owner 更新显示名，不改变身份 ID |
| `devices revoke ID` | Owner 持久撤销及关闭现存控制连接 |
| `status` | 受当前凭证可见范围限制的状态 |
| `identity whoami` | 当前 owner/device 主体，不是 Bot 管理 |
| `doctor` | 检查本次 TLS、凭证和服务端目录就绪 |
| `login create` | Owner 创建一次性网页登录 ticket |
| `activity list [--after N] [--limit 100]` | Owner 查询保留事件，limit 1～500 |
| `activity watch [--cursor N] [--cursor-file FILE] --output ndjson` | Owner SSE 订阅、断线续传 |
| `version` | 程序、协议、编译器与目标平台 |

未实现命令：`share / proxy / services / bot / message / file / peers` 明确返回 `milestone_not_implemented`。

## 当前控制 WebSocket

`GET /api/v1/node/control`，凭证放 `Authorization: Bearer ...`，请求 `X-Bridge-Protocol: 1`、WebSocket 子协议 `slb.control.v1`。使用 Device 凭证，Owner/Cookie 不得代替。浏览器带 Origin 的请求不作为 Node 连接。

```json
{"type":"welcome","protocol":1,"sessionId":"...","heartbeatMs":5000,"timeoutMs":15000}
```

Node 周期发送 `{"type":"heartbeat","seq":1}`；Server 回复 `{"type":"heartbeat.ack","seq":1,"serverTime":"..."}`。序号递增；失联有界关闭；撤销关闭码 1008，并阻止使用旧凭证再连接。

这只是设备控制链路，**没有 TCP 流、服务注册或远程执行消息**。JSON 在 M1 无业务数据帧。不能据此宣称端口转发已经有了。

## SSE / 游标语义

`GET /api/v1/events?cursor=N` 或 `Last-Event-ID`；需 Owner Bearer 或浏览器 Cookie。事件类型：`ready`、`activity`、`resync`、`session.expired`，另有注释心跳。活动 `id` 是全局单调递增序号。

`activity watch` 只有在向 stdout 成功写出事件后才更新显式 `--cursor-file`。崩溃发生在输出与游标落盘之间时可重复交付，消费者按 `seq` 去重；不宣称 exactly-once。`cursor=0` 从当前保留事件开始；非零游标已被压缩则明确报错，要求重新获取快照并选择新的游标，不静默跳过。

CLI 当前不是浏览器 EventSource 的抽象替身；Node 也不负责启动任何 AI。未来 Bot 通过普通 CLI 使用功能，不增加 A2A 或模型识别。
