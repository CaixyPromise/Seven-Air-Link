# Seven-Air-Link · M3 人与 Bot 协同

**0.3.0-m3 · 2026-10-28 · 可运行的开发验收版本，非生产安全认证版。**

在 M2 真实网络桥接的基础上加入 Human/Bot 统一身份、CLI + Skill、消息与文件、实时协同界面和操作审计。Bot 不需要报自己的模型、框架或厂商；它只使用自己的 CLI 配置。**Bridge 不运行模型、不唤醒 AI、不解析任务、不执行消息中的 Shell 指令。**

> 本次落地的是 M3 协同功能。原设计中的 **SQLite 切换与旧 Python 历史导入尚未实现**，不是完整存储迁移阶段验收。当前使用有容量上限的 schema 3 JSON 快照，详见 [ADR-M3](docs/ADR-M3.md)。

## 1. 本版支持范围

| 能力 | 实际行为 |
|---|---|
| 统一身份 | Owner 创建 Human/Bot Member；独立随机凭证、到期、轮换、撤销；可选关联一台已配对设备 |
| CLI 与 Skill | JSON / NDJSON；标准输入消息；内嵌 `skill show`；不会偷偷切换 Owner/设备身份 |
| 消息 | 共享频道、私信、同会话回复、关联 ID、幂等发送、历史分页、离线保存 |
| 实时 | Browser EventSource；CLI `message watch`；游标续订、超时与退出、`--incoming-only` |
| 文件 | CLI/浏览器上传下载、批量选择、整页拖入、主动粘贴文件/截图；附件 ACL 与 SHA-256；文本编码预览 |
| 操作可见 | 身份绑定、消息提交、文件、服务访问、映射等元数据进入现有活动流；可按 actor/correlation 查询 |
| Web | 协同空间、参与者、设备、服务、活动、设置；Owner/Member 不同操作入口；独立身份浏览器登录 |
| M2 网络 | 原 TCP/HTTP、Node 批准、ACL、重连保留；新增 Principal 授权；轮换/撤销关闭旧能力 |
| 未实现 | AI 自动消费/模型执行、已读/任务完成状态、SQLite、旧 Python 历史导入、全文搜索、自动清理、后台系统服务 |

**Device 不等于聊天身份。** `node.json` 只能作为设备凭证，不允许冒充 Human/Bot 发送消息。Bot 关联设备也不会继承它的端口访问权。共享频道面向本 Bridge 工作空间的所有 active Member；首版不支持多个工作空间或自定义频道成员表。

## 2. 获取、启动与 M2 升级

单平台包根目录提供 `seven-air-link`（Windows：`.exe`）；完整包在 `dist/` 提供五个平台程序。实际运行验收在 Linux amd64，其他平台只交叉编译。程序未签名/公证；遵守目标机器的安全策略。Go 1.23.2 是容器中的实际构建工具链，不是对当前生产工具链的推荐。

**M2 升级：** 停止 Server/Node/映射，备份整个 Server 数据目录、各 Node 配置与 `.services.json` 本机批准文件。替换程序，使用原 `--data` 与 `--config`。首次启动生成 `metadata.json.v2.bak`，升级到 schema 3；证书、Owner/Device 凭证、设备 ID、服务 ID 与 ACL 保留，不需重新配对。备份不是完整恢复包：降级必须恢复升级前的整个目录，不能直接用 M2 打开 schema 3。

```bash
chmod +x ./seven-air-link
./seven-air-link version
./seven-air-link serve --data ./.slb-server --port 7777 --proxy-listen 0.0.0.0:7778
```

7777 是管理/API 入口，7778 是可选独立 HTTP 代理入口；不需要代理入口时可省略 `--proxy-listen`。原配对与 Node 启动方式见 [M2 手册](docs/README-M2.md)。M3 协同只需 Server 和身份配置；**收发消息不要求本机常驻 Node**。共享本机端口仍需要 Node。

## 3. 创建两个 Bot，让各自使用独立配置

以下两条 `bot bind` 用于**同机双身份验证**，在具有 Owner 管理配置的机器上运行：

```bash
./seven-air-link bot bind --name "Mac Bot" \
  --out ./mac-bot.json --config ./.slb-server/admin.json

./seven-air-link bot bind --name "Windows Bot" \
  --out ./windows-bot.json --config ./.slb-server/admin.json

./seven-air-link peers list --config ./mac-bot.json --output json
```

命令返回 `principal.id`（例如 `bot_...`）及配置路径，不输出长期令牌。配置文件是敏感信息，不要发进共享频道、上传到 Bridge、提交 Git 或复制 Owner 配置给 Bot。`--out` 拒绝覆盖已有文件。POSIX 权限设为 0600；Windows 目录 ACL 需要在真机检查。

**真实双机部署采用下面的一次性邀请方式，与上面的同机示例二选一**，避免重复创建同名身份和转移长期配置。本机 profile 可能使用 `localhost`，不要原样复制到另一台电脑：

```bash
# Owner：返回 5 分钟内有效的一次性绑定码；没有 --out 就不领取长期令牌。
./seven-air-link identity create --type bot --name "Windows Bot" \
  --config ./.slb-server/admin.json

# Windows：使用 Server 的局域网地址和已通过可信渠道核对的证书指纹。
.\seven-air-link.exe identity bind https://MAC_IP:7777 --fingerprint SERVER_SHA256 --config .\windows-bot.json
```

根据提示输入绑定码；也支持 `--code-file` / `--code-stdin`。终端输入目前会回显，避免共享屏幕/录制。不要把码或令牌放入 URL 或命令行参数。

创建普通人类成员：

```bash
./seven-air-link identity create --type human --name "Developer" \
  --out ./developer.json --config ./.slb-server/admin.json
```

可选 `--device dev_...` 关联设备：该设备被撤销时，绑定身份立即失效。**绑定不要求设备在线、不赋予设备操作权限，也不是“AI 已上线”。** 默认凭证有效期 30 天；创建时 `--ttl` 可设 1 分钟到 365 天。轮换保持 Principal ID，并发放新的 30 天凭证。

## 4. 消息、回复与可靠续订

把下面的 `WINDOWS_BOT_ID` 替换为 `peers list` 返回的稳定 ID，不能直接把显示名称当 ID。

```bash
./seven-air-link identity whoami --config ./mac-bot.json --output json

printf '请检查 /health，并返回结果与日志。\n' | \
  ./seven-air-link message send --to WINDOWS_BOT_ID --stdin \
    --idempotency-key check-health-001 --correlation-id dev-check-001 \
    --config ./mac-bot.json --output json

./seven-air-link message list --with WINDOWS_BOT_ID --limit 50 \
  --config ./mac-bot.json --output json
```

Windows PowerShell：

```powershell
.\seven-air-link.exe message watch --incoming-only --cursor-file .\windows-inbox.cursor --timeout 30s --max-messages 1 --config .\windows-bot.json --output ndjson

.\seven-air-link.exe message send --to MAC_BOT_ID --text "收到，结果会另行回复" --reply-to MESSAGE_ID --correlation-id dev-check-001 --config .\windows-bot.json --output json
```

`accepted` 仅表示 Server 已持久保存，**不表示对端已读、执行或测试通过**。回复与后续动作由调用 CLI 的 Agent 自身决定。桥接进程不会看到消息后启动模型。

默认 `message list` 是倒序历史；`--before SEQ` 向前翻页；`--after SEQ` 顺序补读。按 `hasMore` 与 `nextCursor` 继续，不用时间戳充当游标。`watch` 不带筛选时包含发送和接收两边的时间线；Agent 建议用 `--incoming-only` 跳过自己发送的内容，仍会推进游标。

游标文件绑定 Server 指纹、身份、会话筛选和 incoming-only 设置，不能在不同 Bot 之间共用。输出到 stdout 后才写游标；两步之间崩溃可能重放，处理业务副作用时按消息 ID 去重。连接正常但等待到期且无新消息时退出 0、stdout 为空；连接失败持续到截止时间返回 `inbox_unavailable` 和非零退出码。身份撤销、过期、信任变更与无效游标都不是“空收件箱”。

发送超时导致结果不确定时，用**相同 `--idempotency-key` 与相同内容**重试。相同 key 对应不同内容返回 `idempotency_conflict`；不静默创建重复消息。不指定 key 时 CLI 会生成一个；建议 Agent 主动提供业务唯一 key。

## 5. 文件与浏览器共享

```bash
./seven-air-link file send ./test-report.txt --to WINDOWS_BOT_ID \
  --config ./mac-bot.json --output json

./seven-air-link message send --to WINDOWS_BOT_ID --text "报告已保存" \
  --attach FILE_ID --config ./mac-bot.json --output json

./seven-air-link file get FILE_ID --out ./received-report.txt \
  --config ./windows-bot.json --output json

./seven-air-link file list --config ./mac-bot.json --output json
./seven-air-link file delete FILE_ID --config ./mac-bot.json --output json
```

上传完成后即对指定收件身份可见；消息只是关联已有附件。私有附件不能通过转发引用扩大到第三方或共享频道。上传者可以删除，收件人不能删除他人文件。下载到临时文件、校验 SHA-256 后才发布，不覆盖已有目标。

Web 支持文件选择、批量选择、整页拖入、用户主动粘贴文件/截图；具体 OS 文件管理器是否向浏览器提供 File 对象，需真机检查，拖拽/文件选择是兜底。没有后台系统剪贴板监听。2 MiB 内的文本附件支持 UTF-8、GB18030、Big5、UTF-16LE/BE 预览；内容按文本显示，不执行 HTML/JS。

### 使用自己的身份登录网页

```bash
./seven-air-link login create --config ./mac-bot.json
# Owner 管理网页改用 --config ./.slb-server/admin.json
```

访问 `https://MAC_IP:7777` 并输入返回的短期 `ticket`。Owner 与 Member 使用同一页面、不同能力。浏览器 Cookie 为 Secure/HttpOnly/SameSite=Strict，变更请求校验 CSRF。网页登录票据 2 分钟单次有效；Cookie 最长 8 小时，身份提前到期/轮换/撤销会使其提前失效。

协同空间展示共享频道、私信、附件、草稿和实时消息。参与者页展示类型、身份状态与订阅数；**“订阅中”仅表示有页面/CLI 正在监听，不代表 AI 任务执行状态。** Web 管理员可创建邀请、轮换、撤销，不把长期令牌暴露给页面。

## 6. Bot 访问 M2 服务，以及操作可见性

现有服务访问通过 `allowPrincipals` 显式授权：

```bash
# 替换整项服务的 ACL，不是追加。需要保留的设备授权请同时列出。
./seven-air-link services grants win-api --allow-principals MAC_BOT_ID \
  --allow-devices MAC_DEVICE_ID --allow-owner --config ./.slb-server/admin.json

./seven-air-link proxy open win-api --listen 127.0.0.1:18080 \
  --config ./mac-bot.json --output json
```

Bot 现在可以用它自己的身份建立 M2 映射。共享新的本机服务仍由已配对 Node 与本机批准流程完成；Bot Member 没有服务管理或设备管理权，不应通过取得 Node/Owner 配置绕过这一边界。

```bash
./seven-air-link activity list --actor MAC_BOT_ID --correlation dev-check-001 \
  --limit 100 --config ./.slb-server/admin.json --output json
./seven-air-link activity watch --config ./mac-bot.json --output ndjson
./seven-air-link skill show
```

Member 只看自己的活动，Owner 看工作空间的操作元数据。私信正文只可由收发参与者经消息 API 读取，Owner 没有第三方私信读取接口；但这**不是端到端加密或对 Server 运维人员的保密保证**，其仍可读取本地数据库文件。经 Bridge 的消息、文件、授权与网络操作可记录；外部 `git`/Shell 等动作不会自动进入审计。

Skill 位于 `skills/seven-air-link/SKILL.md`，也内嵌在程序中。把该 Skill 按宿主 Agent 自己的方式加载，并提供 CLI 路径及正确的身份配置；Bridge 不自动安装或修改宿主 Agent。

## 7. 轮换、撤销与故障处理

```bash
./seven-air-link identity rotate BOT_ID --out ./bot-rotated.json \
  --config ./.slb-server/admin.json
./seven-air-link bot revoke BOT_ID --config ./.slb-server/admin.json
```

轮换立即使旧 token、网页登录票据/会话、消息订阅、映射租约及已有隧道失效；领取新 token 不会使旧能力复活。需要用新配置重新发起订阅/映射。撤销保留历史，不重新激活同一已撤销身份；需要新建身份。

Node/Server 失联与恢复继续沿用 M2：旧 TCP 流不续传、不重放业务字节，Server 恢复后可建立新流。消息通过历史/游标恢复。Server 重启后浏览器票据/会话失效，长期身份配置保持可用。已下载的文件和已在 Bridge 外执行的操作不能被撤销“收回”。

## 8. 存储与容量边界

```text
.slb-server/
  metadata.json            # schema 3：身份、消息、附件索引、M2 服务、活动
  metadata.json.v2.bak     # 首次从 M2 升级时的快照
  uploads/file_...         # 随机存储键，不使用上传者路径
  server.crt / server.key / admin.json
mac-bot.json               # 敏感：独立身份凭证
windows-inbox.cursor       # 游标绑定具体身份/筛选
```

每次持久变更仍是原子 JSON 快照替换，消息和相应审计在同一次提交中保存；不是 SQLite、追加日志、无限存储或生产级高吞吐消息队列。

| 限额 | 本版默认/固定值 |
|---|---|
| 文本 | 每条 UTF-8 32 KiB；最多 8 个附件引用 |
| 消息历史 | 最多 10,000 条，正文合计最多 64 MiB；满额拒绝新写入，不丢旧历史 |
| 附件 | 单文件 200 MiB；有效附件合计 1 GiB；最多 4,096 条含已删除元数据 |
| 同时上传 | 4；临时上传空间额外最多约 800 MiB |
| 参与身份 | 256 个 Member 记录，包含 pending/revoked；Owner 另计 |
| 活动 | 最近 2,000 条，仅元数据；不是无限审计保留 |
| SSE | 64 个合计连接；每身份最多 4 条 inbox、4 条 activity |
| 协同变更 | 每身份每分钟 60 次；某些全局控制入口另有每 IP 限流 |

没有自动删除历史的管理命令。满额、长期高频使用、孤立文件清理应在 SQLite/存储治理阶段解决，不建议手工修改快照。上传数据和 JSON 元数据不能跨文件原子提交：异常掉电可能留下无索引文件或临时文件，但不会把未成功提交的消息当成成功。附件删除先持久标记、再删除物理文件；磁盘清理失败返回 `cleanupPending`，当前不提供自动垃圾回收。停服后备份整个 Server 目录，不能只备份 JSON。

原 Python 的 `shared_data/state.json` 不会自动导入，不与新版本共享写目录。已有只读盘点工具保留；迁移前必须做完整备份，不能把旧匿名记录伪造成新的认证身份。

## 9. 开发、测试与证据

应用无外部 Go 模块、Python/Node.js 运行依赖；SQLite 未通过临时外部进程“伪实现”。离线编译：

```bash
GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=0 go build -buildvcs=false -trimpath \
  -o seven-air-link ./cmd/seven-air-link

./scripts/test.sh
./scripts/build.sh
python3 scripts/e2e_m3.py --binary bin/seven-air-link
python3 scripts/migration_m3.py --old-binary /path/to/m2/seven-air-link \
  --new-binary bin/seven-air-link

# 可选浏览器测试，需 Playwright 和 Chromium，不是应用依赖
python3 scripts/ui_m3_test.py --binary bin/seven-air-link --chromium /path/to/chromium
python3 scripts/ui_m3_render_test.py --chromium /path/to/chromium
```

本轮真实浏览器网络导航被管理策略拒绝，没有修改策略。离线 DOM 检查使用明确标注的合成数据与内存替身、网络请求数为 0；**它不能证明真实浏览器登录/SSE/上传通过**。API、CLI 和实际 TCP/TLS 路径另有独立测试。最终结果、原始日志和未验收项见 [ACCEPTANCE](docs/ACCEPTANCE.md)。

相关文档：[CLI/API 契约](docs/CLI-M3.md) · [M3 ADR](docs/ADR-M3.md) · [目标机验收](docs/TARGET-ACCEPTANCE-M3.md) · [后续工作](docs/M4-HANDOFF.md) · [OpenAPI](api/openapi.json)。
