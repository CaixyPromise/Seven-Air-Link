# 0.1.0-rc.4 — 单 Server 与独立客户端

这是待跨平台 CI 验收的身份边界修复源码；发布状态以 GitHub 实际结果为准。
旧 rc.1/rc.2/rc.3 标签不得移动。详见 [身份设计](IDENTITY-BOUNDARIES.md)。

## 升级

先停止旧 Server，备份整个旧数据目录。在旧版本目录执行 `storage backup`
或停服后完整复制。换用新程序、仍指向相同的 `--data`。第一次打开将 schema
5 迁移到 6；保留数据、ID 和凭证，**旧 Owner 固定密码被禁用**。
回滚只能恢复升级前完整备份（含证书与凭证），不能用旧程序读新数据库。
备份会恢复当时的授权，包括已在备份后撤销的凭证；恢复后检查授权。
如果 rc.3 密码曾分发给不可信访问者，升级后还应审查已经创建的身份、设备、
服务 ACL 和凭证；迁移不会撤销之前已经发生的管理操作。

## Mac 是唯一 Server

```bash
./seven-air-link serve --data ./server-data --port 7777
```

另开 Mac 终端，设置局域网加入密码，再生成仅用于本机管理的 Ticket：

```bash
./seven-air-link auth password set --config ./server-data/admin.json
./seven-air-link login create --config ./server-data/admin.json
```

Mac 浏览器打开 `https://localhost:7777`，选择“一次性凭证”使用 Ticket。
Server 本机的 Owner 菜单会显示设备管理、密码设置和存储诊断。只打开
localhost 而没有 Owner 凭证不会获得管理权；本机用加入密码登录也是普通成员。

## Windows 浏览器加入

打开 `https://MAC_IP:7777`，核对并信任此 Server 证书，填写名称与加入密码。
Windows 得到自己的 Human Member，看到协同空间、便签、参与者、获准服务和
自己的连接；不会获得 Owner。纯 Web 协同无需安装 Node，也无需执行 `serve`。

## Windows CLI：显式绑定，或单独加入

推荐在 Windows 页面“连接设置 → 绑定本机 CLI”生成两分钟单次绑定码，
然后在 **Windows 本机**运行（替换 Mac IP 和已独立核对的证书 SHA256）：

```powershell
.\seven-air-link.exe client bind https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name "Windows CLI" --config .\windows-client.json
```

按提示输入绑定码。它与浏览器共享 Principal，但不是同一份凭证，均可单独撤销。
不需要共用浏览器身份时，直接使用加入密码建立另一个独立 CLI 身份：

```powershell
.\seven-air-link.exe client login https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name "Windows CLI" --config .\windows-client.json
```

两个命令二选一；不会覆盖已有配置，不要复制 Mac 的 admin.json。

```powershell
.\seven-air-link.exe identity whoami --config .\windows-client.json --output json
.\seven-air-link.exe services list --config .\windows-client.json --output json
.\seven-air-link.exe proxy open MAC_SERVICE_NAME --listen 127.0.0.1:18080 --config .\windows-client.json
```

映射监听在 Windows；它只能访问该身份已授权的 Service。命令保持前台运行，
退出或撤销会关闭监听。不存在第二个 Server。

## Windows 共享自己的本地端口（可选 Node）

```powershell
.\seven-air-link.exe client node --name "Windows-PC" --out .\windows-node.json --config .\windows-client.json
.\seven-air-link.exe node start --config .\windows-node.json
```

另一终端执行 `share http/tcp`，使用 `windows-node.json` 而不是 Member 配置。
Node 是本地网络代理，不是聊天身份，也不代表创建新协同空间。共享目标默认
数字回环 IP，权限需明确列出。浏览器可通过“连接本机 Node”生成配对命令。

## Bot 与便签

```powershell
.\seven-air-link.exe bot bind --name "Windows Bot" --out .\windows-bot.json --config .\windows-client.json
.\seven-air-link.exe note get --config .\windows-bot.json --output json
.\seven-air-link.exe note set --revision REV_FROM_GET --file .\note.txt --config .\windows-bot.json --output json
```

Human 只管理自己创建的 Bot；Bot 不继承其父身份的服务授权，更不会继承 Owner。
便签仍自动保存、SSE 同步和乐观锁，来源包含操作者、Client/Session、观察到的 IP。
Bot 与 CLI 不需要 A2A 或独立模型适配。

## 撤销

```bash
# Server 本机 Owner：查看全部客户端，撤销某一份凭证
./seven-air-link client list --config ./server-data/admin.json
./seven-air-link client revoke CLIENT_ID --config ./server-data/admin.json

# 普通 Member 用自己的配置，只能查看/撤销自己的连接
./seven-air-link client list --config ./windows-client.json
```

Client、Bot、Device 各自撤销；凭证的外部已完成副作用无法收回。
知道仍有效的加入密码的人可用全新浏览器重新加入，撤销不是物理主机封禁。

## 测试入口

```bash
bash scripts/test.sh
bash scripts/test_identity_linux.sh --binary "$PWD/bin/seven-air-link"
python3 scripts/ui_identity_test.py --binary bin/seven-air-link --chromium /path/to/chromium
python3 scripts/ui_identity_render_test.py --chromium /path/to/chromium
```

第三项是真实浏览器联网，第四项只是显式离线夹具。不能相互替代。
原 `e2e_rc3.py` / `ui_rc3_test.py` 是旧 Owner-password 契约的历史脚本，
当前回归入口已改为 identity 脚本；不应把旧行为继续作为成功标准。
