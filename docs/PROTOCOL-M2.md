# M2 控制、数据与恢复协议

## 1. 公共入口

HTTPS `/api/v1` 为权威资源 API；SSE 保持 M1 的 Owner/浏览器活动订阅语义。浏览器提交变更时必须带原会话的 CSRF 头。设备 Bearer 与 Owner Bearer 可操作各自获授权资源，但不能互相冒充。

`GET /api/v1/node/control`：Device Bearer + `X-Bridge-Protocol: 1` + 子协议 `slb.control.v1`，不接收浏览器 Origin。welcome 增加 `networkProtocol: 1`；没有该字段时，新 Node 只做 M1 心跳，不发送网络扩展。

控制文本消息：
- `welcome` / `heartbeat` / `heartbeat.ack`：保持 M1 序号与静默超时规则。
- `services.ready`：Node 发送当前连接批准的 Service ID 列表（最多64）；Server 交叉核对所有权和未停用状态。
- `tunnel.open`：Server→目标 Node；包含 tunnelId/serviceId/target/sessionId/expiresAt/一次性目标 ticket。
- `tunnel.failed`：目标→Server；只接受与本连接绑定的 tunnelId，以及有限错误代码。

## 2. 建连时序

1. 来源 CLI 显式创建前台本地监听，POST `/forwards`；Server 校验 Service 使用权和目标当前批准状态，签发15秒租约。
2. 本地应用连接监听口，CLI POST `/tunnels {forwardId}`；Server 再检查租约、权限与配额。
3. Server 固定目标服务/版本/目标连接 session，生成5秒有效的两端独立票据；源票据在 API 响应返回，目标票据仅经目标控制通道发送。
4. 目标 Node 检查本机政策，然后 dial 固定回环 TCP 目标；源/目标分别建立数据 WSS。
5. Server 在每次数据 attach 重新校验身份、票据、方向、目标 session、当前 ACL/revision。一次成功 upgrade 消费对应票据。两端均到位后发送 READY。
6. 原样双向传输；FIN 支持单向关闭。异常/撤销/父连接取消立即关闭两端。审计只记录通道与操作元数据。

**控制面确认不是目标健康检查。**Service 显示 online 代表 Node 在线并批准，不保证目标进程每次都监听；目标连接拒绝会准确返回 `target_refused`。

## 3. 数据通道

路径：`GET /api/v1/tunnel/data`。使用 WSS；子协议 `slb.data.v1`。

请求 Header：
```text
Authorization: Bearer <当前调用身份凭证>
X-SLB-Tunnel-ID: tun_...
X-SLB-Tunnel-Ticket: <单次票据>
```

不允许 Origin、URL 携带凭证、文本数据消息、压缩扩展。每个重组后 WebSocket message ≤16,384 bytes，合法 RFC6455 碎片和 ping/pong 不改变字节语义。

二进制 message 的首字节定义：

| 值 | 名称 | 载荷 | 约束 |
|---|---|---|---|
| 0 | READY | 无 | 建立时由 Server 发送；每条数据连接一次 |
| 1 | DATA | 1–16,383 bytes | 任意二进制，按序传输 |
| 2 | FIN | 无 | 发送方向结束；反方向仍可传输 |
| 3 | RESET | 1–64 bytes UTF-8 错误码 | 明确失败；不嵌入业务载荷/凭证 |

例：`[0x01] + payload`；不对业务消息做边界假设，TCP 是字节流。两个方向均 FIN 后清理；底层 EOF/WebSocket Close 未伴随 FIN 属于异常终止而非静默截断成功。写 API 的 deadline 优先于默认30秒写阻塞保护。

HTTP Gateway 复用相同目标流机制，但源在 Server 内部，是经授权的 `net.Conn` / `ReverseProxy` Dial，不创建虚假的客户端票据身份。

## 4. 权限与状态

Service ID/目标固定；ACL revision 变更关闭全服务现存流并使旧浏览器 grant 失效。设备撤销关闭该设备作为源或目标的流。数据票据重放、跨身份消费、跨目标 session 附着均拒绝。失败请求不获得目标连接权限。

服务状态：`online` / `offline` / `unapproved` / `stopped` / `revoked`。停止是持久 tombstone；不自动复活。服务注册恢复只读取当前 Server ACL，不将本机旧授权覆盖过去。

Forward 仅为租约；一条 Forward 可服务多个 TCP 流，默认每3秒续租。租约过期后活跃流也关闭。Owner 可关闭他人映射，普通设备只能关闭自己映射。

## 5. 恢复语义

Node 指数退避（含 jitter）重连；已批准目标按原 Service ID 重新注册并声明 ready。Server 不从持久文件恢复“在线”。Server 每次启动生成 bootId；前台映射只在检测到 bootId 变化时允许自动重建丢失的租约。

同一 bootId 的租约找不到意味着显式关闭/过期，不自动重建，防止撤销被恢复逻辑抵消。暂时的目标 Node 离线不会停止本地监听，新连接快速失败；Node 恢复后可建立新流。

TCP 无字节级重传/断点续传，HTTP 请求不自动重放。客户端必须根据业务幂等性决定是否重试。

## 6. 限流、错误与审计

服务数、映射数、数据流数、票据数、JSON/message大小和写阻塞均有限制。网络相关 CLI 错误非零退出；普通 TCP 调用者得到 EOF/RESET，详情由日志/API说明，不能向任意业务流注入 JSON 错误。

常见码：`access_denied`、`device_offline`、`service_unavailable`、`target_refused`、`local_policy_denied`、`quota_exceeded`、`timeout`、`forward_closed`、`forward_not_found`、`listen_port_in_use`、`invalid_tunnel_ticket`。

结构化 HTTP 错误继承 `error.code/message/requestId/retryable`。隧道、共享、授权与映射状态变化产生活动；没有记录 SQL、文件内容或 TCP 数据包。全局最近2,000条事件为当前存储限制，不声称所有历史永久保存。
