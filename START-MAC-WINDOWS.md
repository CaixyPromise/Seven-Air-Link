# Seven-Air-Link v0.1.0-rc.3 — Mac 启动 / Windows 连接

> **重要：** M4 是原生 SQLite/cgo 版本。当前交付的 Mac/Windows 文件属于**本机编译包**，并非已经通过 Linux 交叉编译的 M4 可执行文件。请分别在原生 macOS/Windows 上运行构建脚本，或使用随包提供的 GitHub Actions 工作流让原生 Runner 构建。不要把另附的 v0.3.0-m3 程序与 M4 共享同一数据目录。

## 一、编译 Mac（Apple Silicon / Intel）

安装 Homebrew、Xcode Command Line Tools 和受支持的 Go，然后：

```bash
xcode-select --install # 如果已经安装可跳过
brew install go sqlite pkg-config
cd seven-air-link
bash ./build-macos.sh
./seven-air-link version
```

脚本自动识别当前 Mac 的 arm64 或 amd64，链接 Homebrew SQLite，测试 SQLite 与 store，输出 `./seven-air-link`。SQLite 动态库需继续存在于 Homebrew 原安装位置。构建过程只做编译与测试，不会自动启动 Server、写入用户凭证或安装系统服务。

## 二、编译 Windows x64

1. 安装受支持 Go 和 [MSYS2](https://www.msys2.org/)。
2. 打开 **MSYS2 UCRT64** 终端执行：

```bash
pacman -Syu
pacman -S --needed mingw-w64-ucrt-x86_64-gcc mingw-w64-ucrt-x86_64-sqlite3
```

3. 在项目目录用 PowerShell 构建：

```powershell
.\build-windows.ps1
.\seven-air-link.exe version
```

输出 `seven-air-link.exe` 与 `libsqlite3-0.dll`，**复制程序到其他目录时这两个文件要放在一起**。脚本会测试 SQLite/store 并运行版本烟雾测试，但不能替代真实跨设备连接验收。若 DLL 有额外依赖，按照 Windows 错误检查 UCRT 运行库。

## 三、推荐：Mac 启动 Server，Windows 加入 Node

### 1. Mac：获取本机局域网 IPv4

```bash
ipconfig getifaddr en0
```

Wi-Fi 不一定是 en0；如无结果可使用 `ifconfig` 查看实际网卡。假设得到 `192.168.1.25`，两台机器必须处于可互通的 LAN/VPN，防火墙允许来自 Windows 到 Mac 的 7777/TCP。不要把服务直接暴露到公网。

### 2. Mac：启动服务端

在 Mac 项目根目录运行：

```bash
./seven-air-link serve --data ./.slb-server --port 7777
```

默认在 LAN 接入，必须保持终端运行。启动输出包含 `fingerprint`（Server TLS 证书 SHA-256 指纹）；请用可信方式把完整指纹与 Mac IP 告诉 Windows。**不要传递 Mac 的 `admin.json`。**

如果还需 Web HTTP 反向代理，则增加 `--proxy-listen 0.0.0.0:7778`，并按需允许防火墙端口 7778。

### 3. Mac：生成一次性配对码

**另外打开 Mac 终端**，项目根目录：

```bash
./seven-air-link pairing create --config ./.slb-server/admin.json --ttl 5m
```

保存返回的 `code`（有效 5 分钟，仅使用一次）。

### 4. Windows：加入并启动 Node

在 Windows 项目根目录 PowerShell，替换实际 IP/指纹：

```powershell
.\seven-air-link.exe join https://192.168.1.25:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config .\windows-node.json
```

命令要求输入 Mac 生成的配对码。成功后：

```powershell
.\seven-air-link.exe node start --config .\windows-node.json
```

**这个 Windows 终端保持运行**。`join` 只是配对，`node start` 才建立持续连接。

### 5. Mac：查看连接状态

第三个 Mac 终端：

```bash
./seven-air-link devices list --config ./.slb-server/admin.json
./seven-air-link doctor --config ./.slb-server/admin.json --output json
```

应看到 Windows Node 在线。如果失败，可先在 Windows PowerShell 检查：`Test-NetConnection 192.168.1.25 -Port 7777`。再检查指纹是否准确、配对码是否过期、Windows 防火墙或 Wi-Fi 客户端隔离。

### 6. 浏览器访问

Mac 浏览器打开：`https://localhost:7777`。Windows 浏览器打开：`https://192.168.1.25:7777`。

生成一次性 Web 登录 ticket（Mac 另开终端）：

```bash
./seven-air-link login create --config ./.slb-server/admin.json
```

在浏览器输入 ticket。由于 Server 使用自签名证书，浏览器首次连接需要按系统策略信任**已核对指纹**的证书；不要盲目接受陌生证书。

## 四、验证端口转发：Windows 的 8080 → Mac 的 18080

### Windows：启动测试 HTTP 服务

```powershell
python -m http.server 8080 --bind 127.0.0.1
```

### Mac：也配对为设备（Server 并不自动算 Node）

Mac 另开终端生成一枚新的配对码：

```bash
./seven-air-link pairing create --config ./.slb-server/admin.json --ttl 5m
./seven-air-link join https://localhost:7777 --fingerprint SERVER_SHA256 --name Mac-Dev --config ./mac-node.json
./seven-air-link devices list --config ./.slb-server/admin.json
```

`join` 会提示输入第二枚配对码。记录列表中 `Mac-Dev` 的 `dev_...` ID，作为 `MAC_DEVICE_ID`。这里仅作为映射来源，不要求在 Mac 再常驻运行 `node start`。

### Windows：共享端口给 Mac-Dev

Windows 另开 PowerShell，保持 Windows Node 运行：

```powershell
.\seven-air-link.exe share http --name win-api --target 127.0.0.1:8080 --allow-devices MAC_DEVICE_ID --config .\windows-node.json
```

### Mac：打开本机映射

```bash
./seven-air-link proxy open win-api --listen 127.0.0.1:18080 --config ./mac-node.json
```

另开 Mac 终端：

```bash
curl http://127.0.0.1:18080/
```

你访问的是 Windows 自己的 `127.0.0.1:8080`，不是 Mac 的 HTTP 服务；Windows 不需要将目标开发服务监听到 `0.0.0.0`。`proxy open` 前台运行，Ctrl+C 后停止。

## 五、Bot 与 CLI

Owner 在 Mac 创建一个 Bot：

```bash
./seven-air-link bot bind --name "Mac Bot" --out ./mac-bot.json --config ./.slb-server/admin.json
./seven-air-link peers list --config ./mac-bot.json --output json
./seven-air-link skill show
```

Windows 的 Bot 应由 Owner 创建一次性邀请，在 Windows 用 `identity bind` 领取自己的独立身份配置，而不是把 Mac Bot 的长期 token 复制过去。详见根目录 README。

## 六、升级/数据隔离说明

- M4 Server 启动前先完整备份任何现有 M3 数据目录；原 M3 schema 3 转 M4 schema 4 是单向迁移，回滚需完整备份。
- M4 Server 首次运行会新建 SQLite `bridge.db`；`server.key`、`admin.json` 和设备配置都是敏感文件。
- 如临时使用另附的 M3 直运行包，建议独立目录，如 `.slb-m3-server`，不要与 M4 共用数据目录。
- 当前打包脚本只在 Linux 容器做过静态检查；Mac/Windows 的 SQLite 真实构建与运行仍须在对应平台执行。没有数字签名/苹果公证。

## rc.3 password and notepad

Keep Server running and use `seven-air-link auth password set --config ./server-data/admin.json` once. Then Windows can use fixed-password Web login. Open **共享便签** for autosave/SSE. Details: [RC3.md](docs/RC3.md).
