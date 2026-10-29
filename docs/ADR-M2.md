# M2 实现决策与未关闭风险

版本 0.2.0-m2；2026-10-28。以下是实际代码的边界，不替代完整设计基线。

## ADR-M2-01：继承 M1，渐进实现网络层

保留现有单二进制、TLS pin、Owner/Device 凭证、心跳、事件、原子 JSON 持久化。新增 service/forward/tunnel 资源与独立本机执行控制口；不借网络工作提前实现 M3 的 Bot/消息/文件系统。主阶段测试保留 M1 黑盒回归。

元数据 schema 1→2 先保存原始 `metadata.json.v1.bak`，只新增服务定义；在线连接、隧道、票据、租约继续是运行态。SQLite 属于后续持久层替换，不虚报已实现。

## ADR-M2-02：Principal 与 Device 不混为一谈

M2 只有既有 Owner Human Principal，不创建虚构 Bot/Member。Service 记录 `ownerPrincipalId` 与 `ownerDeviceId`，授权分为 `allowPrincipals` 与明确设备委托 `allowDevices`。日志保留实际调用主体；设备委托不是另一个“人类用户”。

Owner 能管理 ACL，但不能自动使用所有服务。`--allow-owner` 才给予代理访问权；共享设备自身可用，其他设备须明确授权。后续 Bot 可增加 Principal 凭证和 ACL 而不改变隧道协议。没有 A2A、专用 Agent 会话或模型适配。

## ADR-M2-03：本机批准不能被 Web 管理记录替代

`share` 必须使用本机设备配置，访问 Node 创建的随机回环 API。端点和随机凭证写在权限收紧的 `.node.json`；API 拒绝 Origin、错 Host、无令牌和全网卡目标。批准保存在本机 `.services.json`。

Server 注册成功不等于目标可用。只有当前控制连接宣告 `services.ready` 后，服务才能参与转发；每次 `tunnel.open`，Node 再核对准确 Service ID 与固定 target。重新注册同一 ID 不改写 Server 上更新后的 ACL；停用记录不能被重连复活。

目标与源监听仅支持数字回环 IP，不使用 DNS，也不提供“允许任意内网地址”的开关。权限撤销与本机停止关闭存量流。

## ADR-M2-04：前台源端进程，不隐藏守护程序

`node start` 承担共享侧目标执行者；`proxy open` 自己作为临时源端 Node 承担本机监听和数据连接。它不要求同机再起第二个长期数据代理，也不偷偷 fork 后台。浏览器无法开启所在电脑监听口，因此页面提供明确 CLI 命令与租约管理。

`proxy open` 返回源监听地址和租约 ID，3 秒续租/15 秒租约；Ctrl+C 或远端停止有明确清理。Server 重启通过 boot ID 识别后可重建同一端口的租约；同一 Server 的租约删除/过期视作关闭，不自动复活。旧 TCP 流不续传，业务请求不重放。

## ADR-M2-05：独立数据 WS、FIN/RESET 和有界工作量

每条 TCP 流在源与目标各有一条专用 WSS，通过 Server 配对。HTTP Gateway 作为源内嵌到 Server，仅需目标数据 WSS。控制消息仍是短 JSON，数据是二进制；不将 TCP 数据塞入控制消息或审计。

数据端点使用独立子协议和身份/方向/服务/会话绑定的短期单次票据；认证放 Header，不放 URL。消息最大 16 KiB，DATA 最多 16,383 字节，分块写与有界拷贝建立背压；FIN 仅关闭一个方向。WebSocket Close 未收到 FIN 不被解释为正常 TCP EOF。

控制连接、连接建立、传输写阻塞、并发数分别设限。不要求无数据的正常长连接频繁发送业务数据；数据写阻塞上限默认 30 秒。服务并发默认 64、总通道 256；这不是吞吐保证。

## ADR-M2-06：HTTP 代理与管理台必须不同来源

原方案的同管理端口 `/proxy/...` 仅在可信静态内容时看似方便；代理开发页面可能执行 JavaScript，不能与管理 UI 共用来源。因此本版改为显式 `--proxy-listen` 的独立 HTTPS 端口，代理端口不路由管理 API。

浏览器访问由 2 分钟的一次性服务票据换 30 分钟单服务 Cookie。Cookie HttpOnly/Secure/SameSite，Path 限定服务；实际 ACL、服务 revision、设备撤销仍在每次请求检查。跨管理端口 POST 仅允许指定登录入口，其他 Origin 按策略拒绝。

代理不会向上游发送 Bridge Authorization/Cookie/X-SLB-*，也不转发上游 Set-Cookie。完整业务 Cookie、Basic/Bearer 认证、任意前端绝对路径、HMR 兼容性交给 TCP 映射。**这是刻意保留的边界，不是所有 Web 应用均透明适配。**

多个代理服务仍共享代理端口来源；路径/Cookie scope 不能阻止恶意同源页面请求其他服务。该入口仅面向可信开发服务，不能当敌对网站隔离沙箱。管理源隔离、服务端 ACL 与应用间浏览器隔离是三个不同概念。

## ADR-M2-07：本轮传输库取舍与发布门禁

按照 M2 交接要求评审了成熟库方向（Gorilla WebSocket）。当前容器不能经常规 Go 模块渠道下载依赖；本版没有将网上文本拼装为未核验供应链依赖。沿用 M1 的受限 RFC6455 实现，增加独立二进制通道、严格大小限制、碎片处理和写阻塞控制。

测试包含 Go 对 Go 的真实网络、独立 Python websockets 互通、异常帧、race、短时 fuzz。上述证据只支持开发验收，**不代表完整 RFC6455 测试套件通过、成熟库替换完成或生产协议安全审计完成**。

正式发布门禁：取得并锁定成熟库供应链，评估替换适配层；或对当前实现进行独立协议审计与持续模糊测试。使用受支持 Go 工具链重建与漏洞扫描，补齐 Windows/macOS 真机及浏览器联网验收、凭证 ACL、签名/公证。不得把缺少下载条件解释为长期自研协议优于成熟库。

参考（工程选型来源）：
- https://pkg.go.dev/net/http/httputil#ReverseProxy
- https://pkg.go.dev/github.com/gorilla/websocket
- https://go.dev/dl/

## 其余明确限制

Server 单节点权威，不做 HA/P2P；所有流量经 Server 中继，不提供端到端不信任 Server 的加密模型。两段 TLS 保护传输，但 Server 可读取转发明文；应用本身的 TLS-over-TCP 则继续保留业务端到端 TLS。必须信任 Server 及其管理人员。

本地持久文件不是加密数据库；JSON 每次元数据/活动变更写快照，默认最多 2,000 条活动。长时间大规模建连的磁盘开销、容量与审计保留应在 SQLite 阶段处理。当前资源快照不是泄漏证明或性能 SLA。
