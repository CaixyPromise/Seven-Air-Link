# Seven-Air-Link · M2 核心网络

**版本：0.2.0-m2 · 2026-10-28 · 可运行的开发验收版本，非生产安全认证版。**

在 M0/M1 的 TLS、设备配对、Node 心跳和管理界面上，增加显式共享的 HTTP/TCP 服务、回环端口映射、独立来源 HTTP 代理、授权、撤销和断线恢复。一个 Go 二进制，无 Redis、外部数据库或 Agent Runtime。无 A2A，也不会自动启动 AI 或执行远程 Shell。

> **首选路径：本地 TCP 映射。**它保留应用自己的 HTTP、Cookie、WebSocket、TLS 等字节。独立 HTTP 代理是无状态应用/API 的便捷入口，不是任意网站的透明托管平台。

## 1. 本阶段完成与未完成

| 能力 | 0.2.0-m2 行为 |
|---|---|
| 服务注册 | `share http/tcp`；目标机器 Node 本机批准；数字回环 IP；配置持久保存 |
| 服务权限 | 所属设备自身 + 显式 `allowDevices` / `allowPrincipals`；Owner 管理权不等于使用权 |
| TCP 转发 | `proxy open` 前台监听调用机器回环端口；真实双向字节流；FIN 半关闭 |
| HTTP 代理 | 单独 HTTPS 端口；只代理已授权 Service ID；支持响应流/SSE/WebSocket Upgrade |
| 连接恢复 | Node 重连重新宣告本机批准；Server 重启后映射 CLI 重建租约、保持本地端口 |
| 撤销 | 服务停止、ACL 变更、设备撤销会关闭存量流；新连接重新授权 |
| 操作可见 | 服务/映射/通道活动、错误原因、在线状态、活动 SSE；不记录业务字节 |
| Web | 服务列表、权限编辑、停止、映射命令、独立代理凭证、映射租约管理 |
| 尚未实现 | Bot/member 创建、聊天/文件 API、SQLite、旧历史导入、UDP、P2P/VPN、系统服务安装 |

**“连接恢复”不是 TCP 字节续传。**断线时旧 TCP 流失败，应用需要重新连接；程序不会偷偷重发 HTTP POST、数据库请求等业务操作。

## 2. 获取与运行

单平台包根目录包含 `seven-air-link`（Windows 为 `.exe`）。完整包包含 `dist/` 下五个平台程序。源码包不含二进制，编译命令见第 10 节。Mac/Linux 首次使用：

```bash
chmod +x ./seven-air-link
./seven-air-link version
```

已在 Linux amd64 运行验收；macOS arm64/amd64、Windows amd64、Linux arm64 **仅完成交叉编译**，没有假设你的防火墙、系统权限、签名信任已经验证。二进制未签名/公证。采用容器可用的 Go 1.23.2 构建，正式发布前需受支持工具链重建和漏洞检查。

## 3. 从 M1 升级

停止原 Server、Node 和旧映射；备份整个 Server 数据目录及各设备配置。用 M2 替换各机器程序，**保留原 `admin.json`、设备配置和证书**，不需要重新配对。服务端使用原 `--data` 路径即可。

首次打开 M1 的 `metadata.json`，会原样备份为 `metadata.json.v1.bak`，再写入 schema 2。备份只包含升级时的元数据，不替代完整证书/密钥备份。M1 无法读取 schema 2；降级须停服并恢复升级前完整目录，不能直接启动旧程序读取新文件。

M2 Node 只在 Server 的 welcome 明确声明 `networkProtocol: 1` 后发送网络扩展。旧节点的基本连接兼容不等于具备转发能力；要使用本阶段功能，应升级 Server 和参与转发的 Node/CLI。

## 4. 从零建立两机连接

### 4.1 Mac：Server（持续前台运行）

```bash
./seven-air-link serve --data ./.slb-server --port 7777 --proxy-listen 0.0.0.0:7778
```

7777 是管理 API/UI；7778 是可选 HTTP 代理入口，使用同一证书但不同来源。**不需要便捷 HTTP 代理时，省略 `--proxy-listen`，TCP 转发照常工作。**不要把两个入口配置到同一端口。

在可信局域网按需放行 Server 的 7777；启用代理时另放行 7778。目标开发服务仍只监听它自己的回环地址。公司网络隔离、访问策略和系统防火墙不会被绕过。

### 4.2 为 Windows 配对

Mac 另开终端：

```bash
./seven-air-link pairing create --ttl 5m --config ./.slb-server/admin.json
```

Windows PowerShell，替换 `MAC_IP` 和 Server 终端输出的 `SERVER_SHA256`：

```powershell
.\seven-air-link.exe join https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config .\windows-node.json
.\seven-air-link.exe node start --config .\windows-node.json
```

`join` 根据提示输入一次性配对码；`node start` 保持终端运行。初次指纹需通过可信渠道核对。不要复制 Owner 的 `admin.json` 给 Windows。

### 4.3 为 Mac 配对本机身份

Mac 再生成新配对码，然后在另一终端运行：

```bash
./seven-air-link pairing create --ttl 5m --config ./.slb-server/admin.json
./seven-air-link join https://localhost:7777 --fingerprint SERVER_SHA256 --name Mac-Dev --config ./mac-node.json
./seven-air-link node start --config ./mac-node.json
```

Server 本身不是 Node。在管理终端取得 Mac 的设备 ID（`dev_...`）：

```bash
./seven-air-link devices list --config ./.slb-server/admin.json
```

## 5. 核心闭环：Windows localhost → Mac localhost

### 5.1 Windows 运行开发服务并明确共享

用于验证可先在单独目录运行（实际项目替换它）：

```powershell
python -m http.server 8080 --bind 127.0.0.1
```

另一 Windows 终端，Node 已运行。替换 `MAC_DEVICE_ID`：

```powershell
.\seven-air-link.exe share http --name win-api --target 127.0.0.1:8080 --allow-devices MAC_DEVICE_ID --allow-owner --config .\windows-node.json
```

- `--target` 是**Windows 本机**回环地址，不是 Server 的 localhost。
- `--allow-devices` 只授予指定设备使用权。省略时只有共享设备自身可用。
- `--allow-owner` 单独授予管理身份使用权，供管理台打开 HTTP 服务。省略不影响已授权的 Mac 设备做 TCP 映射。
- `share` 调用本机 Node 的受保护管理口；仅在远端 API 写一条记录不能让 Node 打开未经批准的端口。
- 当前名称在 Bridge 中全局唯一（已停止名称可重新共享为新 ID）。使用小写字母、数字、短横线，最多 48 字符。

### 5.2 Mac 查看并建立映射

```bash
./seven-air-link services list --config ./mac-node.json --output json
./seven-air-link proxy open win-api --listen 127.0.0.1:18080 --config ./mac-node.json
```

保留映射终端运行。另开终端或浏览器：

```bash
curl http://127.0.0.1:18080/
```

实际链路为：`Mac:18080 → Mac CLI 的源端数据通道 → Server → Windows Node → Windows:8080`。

`proxy open` 自己承担前台源端 Node 的网络职责，不额外安装守护进程；来源设备完成配对即可，源端常驻 Node 不是硬依赖。**共享侧的 Node 必须在线**。不指定 `--listen` 时使用 `127.0.0.1:0` 自动分配端口，并在 stdout 返回实际地址。Ctrl+C 停止映射。

### 5.3 其他 TCP 服务

```powershell
.\seven-air-link.exe share tcp --name win-db --target 127.0.0.1:3306 --allow-devices MAC_DEVICE_ID --config .\windows-node.json
```

```bash
./seven-air-link proxy open win-db --listen 127.0.0.1:13306 --config ./mac-node.json
```

数据库客户端连接 `127.0.0.1:13306`。数据库自身账号权限仍需单独配置。Bridge 不解析数据库协议，不将流量写入审计日志。

## 6. 浏览器管理与 HTTP 代理

```bash
./seven-air-link login create --config ./.slb-server/admin.json
```

浏览器访问 `https://localhost:7777`（或已核对的 Server 局域网地址），按系统策略信任证书，输入短期网页登录 ticket。打开“共享服务”，可查看、复制映射命令、修改 ACL、停止共享和停止映射。

“浏览器打开”会创建一枚 2 分钟内有效、只能使用一次的**单服务访问凭证**，通过表单 POST 到独立代理入口；成功后得到 30 分钟的单服务 Cookie。凭证不放 URL。CLI 等价入口：

```bash
./seven-air-link services open win-api --config ./.slb-server/admin.json
```

代理会话独立于管理台会话：退出管理台不会立即撤销已签发的服务访问权；到期、ACL 变更、停止服务或 Server 重启会使其失效。

返回 `loginURL`、`serviceId`、`ticket`。在该页面输入，而非把 ticket 拼进链接。最终路径类似 `https://MAC_IP:7778/proxy/svc_.../`，路径使用服务 ID。

**HTTP 代理的明确边界：**

- 管理台与代理端口不同；代理端口不提供管理 API。
- 不转发请求中的 `Authorization`、Cookie、`X-SLB-*`；也不接受上游 `Set-Cookie`。需要业务鉴权 Cookie/Authorization 的应用请走 TCP 映射。
- 支持 SSE 流式返回、WebSocket Upgrade、基本同目标绝对/根相对重定向改写；不修改 HTML/JavaScript 内的绝对资源路径，也不保证任意 SPA、HMR 或多步登录可在路径前缀下运行。
- 多个服务在代理入口仍是**同一浏览器来源**，路径不是安全隔离。仅用于可信开发应用；不应将敌对/未知页面混在同一代理入口。它与管理台来源隔离，不代表应用之间完全隔离。
- 代理使用 HTTP 连接目标。需要访问目标 HTTPS 时，注册 TCP 并用本地映射保留 TLS，按业务应用的证书规则校验。

## 7. 权限、停止与断线

```bash
# 替换授权列表；省略的授权会被清除，不是追加操作
./seven-air-link services grants win-api --allow-devices MAC_DEVICE_ID --allow-owner --config ./.slb-server/admin.json

# 停止一个本地映射（ID 由 list 返回）
./seven-air-link proxy list --config ./mac-node.json
./seven-air-link proxy close FORWARD_ID --config ./mac-node.json

# 从管理端停止共享
./seven-air-link services stop win-api --config ./.slb-server/admin.json

# 或在共享机器本机停止并禁用本地批准
seven-air-link share stop win-api --config windows-node.json
```

ACL 修改会关闭这项服务的**所有现有流**，即使部分身份仍获授权；旧代理 Cookie 也失效。新的连接按新 ACL 建立。共享设备本身始终保留自己的使用权，彻底关闭需停止服务或撤销设备。

远端停止保留不可自动复活的服务记录；Node 重连不会用旧配置恢复已撤销授权。重新共享需本机明确操作（必要时先 `share stop`），生成新 Service ID。

| 故障 | 程序行为 |
|---|---|
| 已知目标离线 | 新连接立即拒绝并记录 `device_offline`；映射监听保留，目标恢复后可再连 |
| 目标端口拒绝 | `target_refused`，不静默等待 |
| Server 重启 | Node 退避重连；前台映射检测新 boot ID 后重建租约；旧流不能续传 |
| Owner 主动关闭映射 | 存量流关闭；映射进程在下一次续租时退出，不自动重新创建 |
| 凭证/授权撤销 | 拒绝新连接，关闭已有流，相关进程返回失败 |
| 普通 TCP 客户端 | 收到连接关闭/复位，不注入自定义错误文本破坏业务协议；具体原因看 CLI stderr/活动 |

默认控制心跳 5 秒、静默超时 15 秒；数据建连预算 5 秒。前台映射每 3 秒续租、租约 15 秒；同一 Server 上租约已经过期时不会自动复活，需重新执行 `proxy open`。网络恢复不重试业务字节。

## 8. 数据与本机批准

```text
.slb-server/
  metadata.json          # schema 2：M1 元数据 + 服务定义/ACL/停止记录
  metadata.json.v1.bak   # 从 M1 升级时保留的原始快照（如果发生升级）
  server.crt / server.key / admin.json
windows-node.json                # 设备凭证
windows-node.json.services.json  # 本机明确批准的服务目标
windows-node.json.node.json      # Node 运行期本机端点/随机凭证，正常退出移除
```

仍是版本化 JSON 快照，活动最多保留 2,000 条；不是 SQLite，也不是完整长期审计系统。上传文件、旧聊天历史仍归原 Python 版本。不要共用原 `shared_data` 目录。停服后备份证书、凭证与元数据的完整集合；本机文件权限和 Windows ACL 需真机确认。

运行态（在线连接、票据、前台映射租约）不保存成假在线。HTTP 访问票据/会话和数据票据在 Server 重启后失效。

## 9. 限制与安全默认

只允许数字回环 IP（例如 `127.0.0.1`、`[::1]`），不解析任意域名，不允许 `0.0.0.0` 监听、不代理任意 URL、不扫描内网。没有绕过开关。保护目标是显式授权的本机开发服务，不是通用 VPN。

服务端默认 256 条总数据通道、64 条/服务、128 条/身份；本机前台映射/目标 Node 各自有 128 条工作流上限。可使用 `--max-tunnels`、`--max-per-service`、`--max-per-actor`（1–4096）调整服务端上限。注册最多 64 个活动服务/设备；映射最多 32 个/身份、256 个全局。默认数据写阻塞超过 30 秒会关闭，不无限积累内存。

此版继续使用受限、自维护的 RFC6455 实现，数据帧与控制帧分离、总消息 16 KiB；已做独立互通、异常帧、竞态与短时模糊测试，**不等于成熟库替换或安全审计通过**。选型与供应链限制见 [ADR-M2](docs/ADR-M2.md)。

## 10. 构建、测试与文档

应用无第三方 Go 模块，离线编译：

```bash
GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0 go build -buildvcs=false -trimpath -o seven-air-link ./cmd/seven-air-link
```

开发测试需要 Go、C 编译器（race）、Python 3。独立协议测试使用测试专用 Python 包，不是应用运行依赖：

```bash
python3 -m pip install -r scripts/requirements-test.txt
./scripts/test.sh
./scripts/build.sh

# 只重跑 M2 真实进程测试
python3 scripts/e2e_m2.py --binary bin/seven-air-link

# 可选浏览器检查；当前容器网络导航被管理策略阻止
python3 scripts/ui_test.py --binary bin/seven-air-link --chromium /path/to/chromium
python3 scripts/ui_render_test.py --chromium /path/to/chromium
```

`ui_render_test.py` 是显式示例数据、零网络的 DOM/视觉检查，不替代 Browser E2E。真实浏览器联网测试在本环境为 BLOCKED。Linux 真实进程测试与五平台构建的原始证据、覆盖率和未验收项在 [验收报告](docs/ACCEPTANCE.md)。

- [M2 CLI 与网络契约](docs/CLI.md)
- [实现决策与安全边界](docs/ADR-M2.md)
- [OpenAPI](api/openapi.json) / [数据通道协议](docs/PROTOCOL-M2.md)
- [目标机验收操作](docs/TARGET-ACCEPTANCE.md)
- [M1 运行手册](docs/README-M1.md) / [M1 原验收](docs/ACCEPTANCE-M1.md)
- [旧 Python 数据迁移规范](docs/MIGRATION.md)

本阶段证明“授权服务能经过真实隧道被访问并正确失败”，不声称完成 Bot 协同、生产安全评估或真实 Mac↔Windows 的验收。
