# Seven-Air-Link · M0 + M1

**版本：0.1.0-m1 ｜交付：2026-10-28 ｜定位：可运行的开发验收版本，不是生产安全认证版本。**

本项目实现“一台 Server + 多台 Node”的设备互联基础。一个 Go 二进制承担 Server、Node、CLI，Web 页面嵌入程序。没有数据库服务、Node.js、Python 或第三方 Go 模块运行依赖。

**已经完成：** HTTPS/WSS、首次证书指纹绑定、一次性配对、独立设备凭证、真实在线心跳、自动重连、设备撤销、设备列表、只含元数据的活动记录、SSE、CLI、基本管理页面。

**尚未完成：** M2 的 TCP 转发/HTTP 反向代理，以及 M3 的 Bot 身份、聊天、文件共享、SQLite、历史导入。未实现的命令明确返回错误，绝不返回假成功。旧 Python 工具保留在 `legacy/`，继续独立使用。

## 1. 选择程序

单平台包的根目录程序可直接运行；macOS/Linux 程序名为 `seven-air-link`，Windows 为 `seven-air-link.exe`。

| 系统 | 平台包 |
|---|---|
| Apple Silicon Mac | darwin-arm64 |
| Intel Mac | darwin-amd64 |
| Windows x64 | windows-amd64 |
| Linux x64 | linux-amd64 |
| Linux ARM64 | linux-arm64 |

本次实际运行验收在 Linux amd64；其余平台已交叉编译，**未在对应操作系统实际运行**。程序没有发布者签名/公证，应由你在目标机器上确认系统安全策略。不要把未知二进制放行作为默认操作。

完整包将五个平台程序放在 `dist/` 并带平台后缀。例如 Apple Silicon Mac 先执行 `cp dist/seven-air-link-darwin-arm64 ./seven-air-link`；Windows 用 `Copy-Item .\dist\seven-air-link-windows-amd64.exe .\seven-air-link.exe`。源代码包不含二进制，需先构建。

源码离线构建（Go 1.23 或兼容的更新版本）：

```bash
GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0 go build -trimpath -o seven-air-link ./cmd/seven-air-link
```

本次构建工具链为 Go 1.23.2，不代表当前推荐的生产工具链。正式发布前需要用受支持版本重建、复测及补充漏洞检查。

## 2. Mac 作为 Server，Windows 作为 Node

以下每个“运行中的 Server/Node”都保持对应终端开启。没有暗中安装系统服务或后台守护进程。

### 2.1 Mac：启动 Server

在解压后的程序目录执行：

```bash
chmod +x ./seven-air-link
./seven-air-link serve --data ./.slb-server --port 7777
```

首次启动自动生成证书、Owner 凭证及元数据。输出中的重要字段为：

```text
localURL     本机地址，如 https://localhost:7777
lanURLs      当前证书涵盖的局域网地址列表
fingerprint  证书 SHA-256 指纹，供另一台机器独立核对
adminConfig  本机 Owner 配置文件的绝对路径
```

默认监听 `0.0.0.0:7777`。只做单机测试时改用 `--listen 127.0.0.1:7777`。同网段设备仍须能够访问这个端口；公司网络的设备隔离、防火墙等不由 Bridge 绕过。

### 2.2 Mac：另开终端，生成 Windows 的一次性配对码

```bash
./seven-air-link pairing create --config ./.slb-server/admin.json --ttl 5m
```

输出 `code` 和 `expiresAt`。配对码只能成功使用一次，不能作为长期密码。指纹应从 Server 终端经你信任的方式核对，不能仅相信尚未验证的远端网页。

### 2.3 Windows：配对并常驻连接

在 PowerShell 中，替换 `192.168.1.25` 与 `SERVER_SHA256`：

```powershell
.\seven-air-link.exe join https://192.168.1.25:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config .\windows-node.json
```

根据终端提示粘贴刚才的 `code`，回车。当前终端输入会回显；避免屏幕共享或保留敏感终端录制。也可使用 `--code-file` 读取受保护文件，或 `--code-stdin` 从标准输入读取；不提供把密钥放入命令行参数的选项。

配对成功后运行：

```powershell
.\seven-air-link.exe node start --config .\windows-node.json
```

**`join` 只完成配对；`node start` 才建立在线连接。** 配置文件包含这台设备自己的凭证，不包含 Owner 令牌。不要把 `admin.json` 复制给 Windows。

### 2.4 Mac 也作为 Node：凑齐两个真实节点

Server 本身不算 Node。需要 Mac 与 Windows 都出现在在线设备列表时，在 Mac 再生成一枚新配对码：

```bash
./seven-air-link pairing create --config ./.slb-server/admin.json --ttl 5m
./seven-air-link join https://localhost:7777 --fingerprint SERVER_SHA256 --name Mac-Dev --config ./mac-node.json
./seven-air-link node start --config ./mac-node.json
```

这时应有一个 Server 进程、一个 Mac Node 进程和一个 Windows Node 进程。每个 Node 只需要主动连接 Server，不需要额外打开自己的监听端口。

## 3. 查看和管理

在 Mac 的管理终端执行：

```bash
./seven-air-link devices list --config ./.slb-server/admin.json
./seven-air-link status --config ./.slb-server/admin.json --output json
./seven-air-link doctor --config ./.slb-server/admin.json --output json
./seven-air-link activity watch --config ./.slb-server/admin.json --cursor-file ./activity.cursor --output ndjson
```

`doctor` 检查本次连接的证书指纹、凭证及服务端元数据目录就绪，不会伪称已经验证两台真实电脑的防火墙、端口转发或磁盘可写性。

重命名或撤销指定设备（ID 从列表中取）：

```bash
./seven-air-link devices rename DEVICE_ID --name Windows-Test --config ./.slb-server/admin.json
./seven-air-link devices revoke DEVICE_ID --config ./.slb-server/admin.json
```

撤销会持久失效该设备凭证，主动关闭当前连接；该 Node 以非零退出码停止，不会无限重连。重新授权应生成新配对码及新配置文件。不是删除一行 UI 记录后就自动重获权限。

## 4. 打开浏览器管理页面

先生成短期网页登录凭证：

```bash
./seven-air-link login create --config ./.slb-server/admin.json
```

在浏览器访问 `https://localhost:7777` 或已核对的局域网地址，按系统策略信任**这一台 Server 的证书**后，输入输出中的 `ticket`。它 2 分钟有效且只能使用一次。无需把 Owner 的长期令牌粘贴进页面。

页面可查看设备、生成配对码、重命名、撤销、查看活动和指纹。浏览器使用 `Secure + HttpOnly + SameSite=Strict` Cookie 会话，变更请求还需要 CSRF 头。会话在 Server 重启后失效；Node 凭证不会因此失效。

本环境的管理策略阻止了 Chromium 网络导航，因此没有宣称完整浏览器登录/SSE/撤销 E2E 通过。API 会话/CSRF 和实时流已有自动化测试；DOM 布局、文本转义和移动端另做了**无网络、示例数据**的渲染检查。证据见验收报告。

## 5. 存储、备份和旧工具

```text
.slb-server/
  server.crt       自签名服务器证书（可分发公钥证书，不分发私钥）
  server.key       TLS 私钥，禁止共享
  admin.json       Owner 客户端凭证，禁止共享
  metadata.json    M1 设备、凭证哈希、配对和活动元数据
  server.lock      OS 文件锁，不靠手工删除来“解锁”
mac-node.json      Mac 的设备凭证
windows-node.json Windows 的设备凭证
```

M1 存储明确是**版本化 JSON 快照，不是 SQLite**。先写临时文件、同步并替换；提交失败不会先修改内存授权。元数据不含聊天/文件，默认保留最近 2,000 条活动。SQLite、消息/文件及完整迁移在 M3。

停服后备份整个 Server 目录，保留私钥、证书与元数据的一致性；备份属于敏感数据。不要只拷贝 `metadata.json` 就期望原设备自动恢复连接。

原 Python ZIP 冻结保存在 `legacy/`。Python 继续用自己的 `shared_data` 和端口，例如 8000；Go 使用全新目录和 7777。Go 检测到目录中存在旧 `state.json` 时拒绝初始化。当前仅提供只读迁移盘点，不执行导入：

```bash
python3 scripts/legacy_inspect.py --data /path/to/python/shared_data > legacy-inventory.json
```

退出码 0 为盘点通过，2 为有缺失/孤立附件等待处理事项，1 为无法安全读取。盘点不输出聊天正文、不修改原文件，仍应先停止旧版写入并备份。

## 6. 开发和测试

```bash
# Linux/macOS 开发环境：Go、C 编译器（race）、Python 3
./scripts/test.sh

# 五个平台的离线交叉构建
./scripts/build.sh

# 可选：需要 Python websockets 包，仅用于独立协议互通测试
python3 scripts/interop_test.py

# 可选：需要 Playwright + 可运行且允许本地访问的 Chromium
python3 scripts/ui_test.py --chromium /path/to/chromium

# 仅离线 DOM/视觉检查，不等于上面的真实浏览器测试
python3 scripts/ui_render_test.py --chromium /path/to/chromium
```

独立进程故障脚本使用 `SIGSTOP/SIGCONT`，需在 Linux/macOS 运行，不是 Windows 原生故障脚本。源码中有可供后续运行的 CI 配置，但本次没有在 GitHub 或其他操作系统实际触发 CI。

详细说明：
- [开发验收记录](docs/ACCEPTANCE.md)
- [M1 决策与发布限制](docs/ADR-M1.md)
- [CLI / 控制协议](docs/CLI.md)
- [旧版迁移与回滚](docs/MIGRATION.md)
- [M2 接续任务](docs/M2-HANDOFF.md)
- [OpenAPI 3.1 契约](api/openapi.json)

**本阶段证明的是“设备连接和授权闭环”，不是“已经能访问对端 localhost”。端口转发是下一阶段 M2。**
