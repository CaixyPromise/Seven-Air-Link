# 身份与权限边界 — 0.1.0-rc.4

## 1. 一个 Server，而不是每台机器一套服务

Mac 或 Windows 中的一台执行 `serve`，它是此协同空间唯一的权威 Server。
其余电脑只打开该地址的 Web，或用 CLI 连接该地址。客户端不会因为
`client login` / `client bind` / `client node` 自动启动第二个 Server。

Server 第一次启动在其受保护数据目录中生成 Owner 凭证 `admin.json`。
只有持有该凭证（或它签发的 Owner Web Ticket/会话）并从 Server 本机
回环连接的请求才能管理。操作系统名称、浏览器自称的角色、主机名和 IP
都不能授予 Owner。来自本机但没有凭证仍然是未认证请求。

```
Server A :7777 (SQLite, authority, identity, messages, service routing)
  ├─ A local CLI / browser + Owner credential: management
  ├─ B browser + individual Member credential: collaboration
  ├─ B CLI + new/bound individual credential: authorized operations
  ├─ B optional Node: accesses B's approved localhost services
  └─ Bot + independent token: ordinary participant, no Agent runtime
```

业务服务可以在任意获授权的 Node 上，例如 Windows 的 8080。
这不代表 Windows 上需要第二个 Bridge Server。

## 2. 四类对象不混用

| 对象 | 标识与作用 |
|---|---|
| Principal | Human/Bot，权限与内容归属；Owner 是唯一的管理身份 |
| Client | 一份浏览器/CLI 接入凭证；同一 Principal 可显式绑定多份 |
| Device | 已配对的 Node，负责机器本地网络操作；不是聊天身份 |
| Session | 一个短期浏览器登录实例；不等于设备或新 Principal |

首次通过加入密码登录的每个浏览器都获得新的 Human Member + Client；
两个浏览器即使密码、名称、IP 相同也不同。浏览器的 HttpOnly Secure
`__Host-sal_client` Cookie 保存自己的 30 天凭证，重新登录仍需正确的加入密码。
会话最长 8 小时，Server 重启失效；再次登录保留原身份。
这识别的是浏览器安装/配置的凭证，不是可靠的物理硬件指纹。
清除 Cookie、无痕窗口和另一浏览器是新的客户端。

CLI 的 `client login` 默认建立新身份。Browser 与 CLI 只有经
`client-links` 两分钟单次绑定码才能共享 Principal；CLI 仍获独立凭证。
凭证不从浏览器 JavaScript 复制，不因相同 IP 自动绑定。

## 3. 认证和管理默认规则

- 加入密码只允许加入普通 Member，永不返回 Owner。
- Owner API、Owner Ticket 领取和 Owner 代理访问要求真实 loopback + Owner credential。
- 不信任 `X-Forwarded-For`/`Forwarded` 作为权限依据；拒绝请求里的额外 role/source 字段。
- 未认证请求不查看私有资源；Cookie 写操作继续使用 CSRF；Origin/Host/TLS 检查保留。
- `/me` 返回 `capabilities`，UI 在显示应用前条件渲染管理导航/按钮；API 不依赖它。
- Member 能管理自己的连接、邀请自己的 Node、创建/撤销自己拥有的 Bot；不能管理其他身份或全局策略。
- Bot 不能再创建 Human/Bot、管理加入密码或读取全局存储信息。
- 服务使用仍由 Service ACL 决定；创建 Bot、关联设备或知道 Server 地址都不意味着端口访问权。

| 操作 | 本机 Owner | Human Member | Bot | Device |
|---|---|---|---|---|
| 全局密码/身份/设备/存储管理 | 是 | 否 | 否 | 否 |
| 聊天/文件/便签 | 是 | 是 | 是 | 否 |
| 查看/撤销客户端凭证 | 全部 | 仅自己的 | 否 | 否 |
| 创建/撤销 Bot | 全部 | 仅自己的 | 否 | 否 |
| 配对自己的 Node | 是 | 是，最多八台 | 否 | 否 |
| 注册本机服务 | 经本机 Node | 经自己的 Node | 不直接注册 | 需本机批准 |
| 访问服务 | 显式 ACL | 显式 ACL | 显式 ACL | 显式设备授权 |

## 4. 生命周期与撤销

Client 撤销关闭这份凭证的 Browser 会话、Ticket、未领取 CLI 绑定、
映射租约与现有数据流。撤销对应浏览器当前凭证会清除当前 Cookie。
同 Principal 的另一个 Client 不受影响；Bot 和已配对 Node 各有独立凭证，
不会因为撤销某个 CLI 而一起被撤销。Member 发出的尚未消费 Node 邀请
绑定其签发凭证；该凭证撤销后不能继续消费。

完整 Principal 撤销与 Client 撤销不同：整个身份和它拥有的 Bot 会失效，
已有网络流由运行时检查清理。已配对 Device 仍是独立授权对象，需显式设备撤销。
改变/关闭加入密码仅改变新加入条件，不能用共享密码变更代替定向撤销。

**共享加入密码不是物理设备封禁机制。**知道仍有效密码的人可以在新浏览器
或新 CLI 配置下重新加入为新身份。要禁止其重新加入，需要同时更换/关闭
加入密码；不能靠 IP 黑名单伪装成身份安全。当前仍是单空间共享成员模型。

## 5. 操作来源与执行位置

请求成功认证后由 Server 写入 `Source { clientId, sessionId, deviceId, kind, ip }`。
IP 仅用于解释来源和诊断；Actor 为 Principal，Node 的 Device 与之分开。
便签内容、版本、Actor、Source、审计在同一 SQLite 事务提交。
历史 rc.3 便签没有真实来源记录时保留空来源，不伪造补齐。

`proxy open` 在执行该命令的机器监听本机端口；服务端凭 Service ID 和
调用者 ACL 路由，不允许传任意目标 URL。目标 Node 再校验本机明确批准。
Owner 只管理协调空间，不会因 Web 点击而在另一台电脑凭空执行本机操作。

## 6. 迁移与安全边界

schema 5→6 事务迁移增加 clients 与便签 source；**旧管理员密码被禁用**，
不能偷偷把旧管理秘密变成多人加入密码。Operator 应在本机设置新的加入密码。
原 Owner 凭证、已有成员/Bot/Device token、消息、服务 ACL、附件和便签保留。
旧 Owner 网页会话是进程内状态，重启自然失效；旧 rc.3 不可打开 schema 6。

管理网络位置检查只在直接 B/S 部署成立。**不要把管理入口通过不可信的
本机反向代理/端口中继再次开放到局域网或公网**，否则 Server 看到的是真实
代理进程的 loopback；仍需 Owner credential，但“本机来源”不能当作物理证明。
本版不接入可信代理身份链，不接受 forwarded headers 作为替代认证。

本机 OS 账户能读取 Owner 凭证便处于管理信任边界内。Windows 文件 ACL、
Apple 签名/公证、实体机网络和独立安全审计需要各自验收。TLS 继续强制，
不把“处于 LAN”视为安全保证。

Client credential 默认 30 天，加入创建的 Human 记录 365 天；最多 256 个
Member 记录、2048 个 Client 记录（含撤销项），每身份最多 16 个活跃 Client。
当前无自动记录 GC；达到上限明确拒绝，不静默删除历史。
