# M2 CLI 与连接契约

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

未实现命令：`bot / message / file / peers` 明确返回 `milestone_not_implemented`。网络命令见下节。

## 当前控制 WebSocket

`GET /api/v1/node/control`，凭证放 `Authorization: Bearer ...`，请求 `X-Bridge-Protocol: 1`、WebSocket 子协议 `slb.control.v1`。使用 Device 凭证，Owner/Cookie 不得代替。浏览器带 Origin 的请求不作为 Node 连接。

```json
{"type":"welcome","protocol":1,"networkProtocol":1,"sessionId":"...","heartbeatMs":5000,"timeoutMs":15000}
```

Node 周期发送 `{"type":"heartbeat","seq":1}`；Server 回复 `{"type":"heartbeat.ack","seq":1,"serverTime":"..."}`。序号递增；失联有界关闭；撤销关闭码 1008，并阻止使用旧凭证再连接。

控制连接新增 `services.ready`、`tunnel.open`、`tunnel.failed`。TCP 业务字节只在独立 `slb.data.v1` WSS 通道传输，控制连接不承载业务数据。完整帧与重连语义见 `PROTOCOL-M2.md`。

## SSE / 游标语义

`GET /api/v1/events?cursor=N` 或 `Last-Event-ID`；需 Owner Bearer 或浏览器 Cookie。事件类型：`ready`、`activity`、`resync`、`session.expired`，另有注释心跳。活动 `id` 是全局单调递增序号。

`activity watch` 只有在向 stdout 成功写出事件后才更新显式 `--cursor-file`。崩溃发生在输出与游标落盘之间时可重复交付，消费者按 `seq` 去重；不宣称 exactly-once。`cursor=0` 从当前保留事件开始；非零游标已被压缩则明确报错，要求重新获取快照并选择新的游标，不静默跳过。

CLI 当前不是浏览器 EventSource 的抽象替身；Node 也不负责启动任何 AI。未来 Bot 通过普通 CLI 使用功能，不增加 A2A 或模型识别。


## M2 网络命令（实际实现）

| 命令 | 作用与前置条件 |
|---|---|
| `serve --proxy-listen 0.0.0.0:7778` | 显式启用独立 HTTPS 代理入口，管理端口保持7777；省略则只提供 TCP 映射和管理 |
| `share http --name NAME --target 127.0.0.1:PORT --config NODE` | 必须同机 Node 正在运行；经本机管理口批准并持久保存目标 |
| `share tcp ...` | 同上，只做 TCP 字节转发，不提供 HTTP Gateway |
| `share stop NAME_OR_ID --config NODE` | 本机禁用策略并停止 Server 上共享、关闭目标流；重复本机停止是幂等的 |
| `services list --config CONFIG` | Owner 可管理所有服务；设备只看到自身或获授权服务 |
| `services grants NAME_OR_ID --allow-devices ID1,ID2 --allow-owner` | **替换**整个授权列表；省略某项意味着清除；仅Owner/共享设备可管理 |
| `services stop NAME_OR_ID` | 远端停止服务并保留 tombstone；不会在 Node 重连时复活 |
| `services open NAME_OR_ID` | 为已授权 HTTP 服务创建单次浏览器票据；不启动系统浏览器，不把令牌放 URL |
| `proxy open NAME_OR_ID --listen 127.0.0.1:18080` | 前台监听调用机器；stdout 首次输出租约，stderr 为NDJSON诊断；Ctrl+C 停止 |
| `proxy list` | 查询当前可见映射租约 |
| `proxy close FORWARD_ID` | 关闭自己的映射；Owner可关闭其他映射；流立即关闭，CLI续租时退出 |

`share` 可带 `--allow-devices ID1,ID2` 与 `--allow-owner`。没有模糊的 `--allow` 或 `--allow-all`；Principal/Device 明确分开。M2只有Owner Principal，其ID来自Server，不把设备ID假装成用户ID。

服务名小写字母开头，后续小写字母/数字/短横线，最长48；活动名称全局唯一。目标支持数字回环 IP，不支持域名。IPv6示例：`--target [::1]:8080`（shell按需加引号）。源端默认 `127.0.0.1:0` 自动分配监听端口。

`proxy open` 是前台源端执行器，不要求源端常驻 Node 正在运行；共享侧 Node必须在线。它不自动写系统服务，终端退出即停止。

## 配额与超时参数

`serve --connect-timeout 5s`（范围1–5秒）、`--max-tunnels 256`、`--max-per-service 64`、`--max-per-actor 128`（整数1–4096）。目标Node最多128个工作流、单个本地映射最多128个连接是独立上限。不能通过调大Server选项宣称超过设备侧限制。

建立失败或权限失败退出非零；普通 TCP 数据面不给应用注入JSON。创建成功后单连接失败不会终止整个前台监听，具体原因在stderr；Owner撤销/关闭、同Server租约过期则终止监听。

## 例：不用给予 Owner 业务访问权

```bash
# Windows共享；假设当前目录配置为 node.json
seven-air-link share tcp --name private-db --target 127.0.0.1:3306 --allow-devices dev_MAC --config node.json
# Mac使用它自己的设备配置
seven-air-link proxy open private-db --listen 127.0.0.1:13306 --config mac-node.json
```

此时 Owner 仍可停止服务或编辑权限，但不能直接打开代理。修改ACL是显式、被记录的控制操作。
