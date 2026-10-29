# Historical M4 implementation details

This is engineering history from the previous project identity. Current CLI and release version are Seven-Air-Link 0.1.0-rc.1.

## 1. 本版变化与范围

| 项目 | M4 实际行为 |
|---|---|
| 主存储 | `bridge.db`，schema 4，SQLite WAL，`synchronous=FULL`，单 Server/单序列化数据库连接 |
| 消息与审计 | 消息、幂等唯一约束、关联活动及序号在同一事务提交；历史按 SQL 索引分页，不再全量克隆消息 |
| 其余资源 | 身份/设备/授权/服务/文件按资源分行，保留各资源 JSON 字段；不是把整个状态塞进一个 BLOB |
| 迁移 | schema 1/2/3 JSON → SQLite；原字节备份、SHA-256 封印、已封印迁移中断可恢复、旧版写入保护 |
| 备份恢复 | 停服加锁；`VACUUM INTO` 独立 DB + Server 凭证/证书 + 有效附件；校验清单；只恢复到不存在的新目录 |
| 清理 | 默认预览；删除已删除记录的残留文件、过期上传临时文件、无引用附件；不删除仍引用附件或跟随软链接 |
| 诊断 | `storage check --hash-files`、Owner `doctor` 统计、只读 Web 存储面板；失败返回非零退出码 |
| 本地权限 | Server 目录 POSIX 0700；数据库/凭证 0600；配置/锁/数据库拒绝软链接；备份防路径穿越 |
| 保持原边界 | Bot 普通身份，独立 ACL；Node 本机批准；权限撤销关闭已有流；旧 TCP 流不续传/重放 |

**不包含**：Python `state.json` 历史导入、后台自动 GC、无限历史、全文搜索、系统服务安装、HA/P2P、自动模型消费、生产安全认证。原有容量上限没有因为换数据库就默默放开。

## 2. 运行与构建

Linux 平台包根目录为 `seven-air-link`；完整包程序在 `dist/seven-air-link-linux-amd64`。程序静态链接 SQLite 引擎，但依然依赖 Linux 的 libc/libm/加载器，不是完全静态 musl 二进制。目标 Linux 应核对 `ldd` 与系统兼容性。

```bash
chmod +x ./seven-air-link
./seven-air-link version
./seven-air-link serve --data ./.slb-server --port 7777 --proxy-listen 0.0.0.0:7778
```

7777 为管理/API，7778 为可选独立 HTTP 代理。不需要便捷 HTTP 代理可以省略 `--proxy-listen`。可信局域网仍须遵循防火墙与网络隔离策略。Server/Node/转发默认继续以前台进程运行。

### 构建依赖的重要变化

M4 引入真实 SQLite C 引擎。本次使用系统提供的头文件/静态库，经项目内小型 cgo 绑定调用；没有调用 Python 或 sqlite3 CLI 作为后台数据库，也没有拼装未经核验的外部 Go 模块。

Linux 构建需要 Go、C 编译器、SQLite 开发头文件及 `libsqlite3.a`。程序运行不需要独立 SQLite 服务、Python 或 Node.js。

```bash
# 在自己的受信任开发环境安装系统依赖，例如 Debian/Ubuntu：
# sudo apt-get install build-essential libsqlite3-dev
GOTOOLCHAIN=local GOPROXY=off CGO_ENABLED=1 \
  go build -buildvcs=false -trimpath -ldflags='-s -w' \
  -o seven-air-link ./cmd/seven-air-link
./scripts/build.sh
```

**不再支持之前的 `CGO_ENABLED=0` 五平台直接交叉编译。**本次不提供冒充 M4 的旧 Mac/Windows 可执行文件；这些系统需要相应原生 C/SQLite 工具链构建及真机验证。当前包实际工具链为 Go 1.23.2、系统 SQLite 3.46.1；不是推荐的正式发布工具链。详见 [ADR-M4](docs/ADR-M4.md) 和 `reports/dependency-provenance.json`。

## 3. 从 M3 升级：先完整备份，再迁移

停止旧 Server、Node 与映射，单独备份每台设备的 Node/Bot 配置及 `.services.json` 本机批准文件。以下维护命令使用**新 M4 二进制**；不在运行中的 Server 上备份/迁移。

```bash
# 尚未升级的旧 Server 目录；输出必须是不存在的、位于源目录以外的目录。
./seven-air-link storage backup --data ./.slb-server --out ./backup-before-m4
./seven-air-link storage verify --from ./backup-before-m4

# 显式迁移一次。直接启动新版 serve 也会迁移，但不能代替事前备份。
./seven-air-link storage migrate --data ./.slb-server
./seven-air-link storage check --data ./.slb-server --hash-files

# 使用原目录、原证书和配置继续运行。
./seven-air-link serve --data ./.slb-server --port 7777 --proxy-listen 0.0.0.0:7778
```

成功后保留原 `metadata.json.pre-sqlite.bak`；`metadata.json` 变成 version 4 降级保护标记，权威数据为 `bridge.db`。原设备/身份/服务 ID、令牌哈希、ACL、消息和附件关联保留，不需要重新配对。`storage migrate` 在 DB 已存在时拒绝重复执行，不会覆盖现有数据。

如果以前的目录权限不是 0700，先在确认目录归属后手动修正权限；程序不会静默接管或重置它。迁移失败应保留源 JSON、备份及 `.migrating` 文件，详见 [存储操作手册](docs/STORAGE-M4.md)。

**Server 备份不包含其他机器上的 Node/Bot 配置。**恢复网络共享时仍需原设备凭证、本机批准和对应 Node 进程。

## 4. 日常诊断、清理与备份

在线 Owner 诊断：

```bash
./seven-air-link doctor --config ./.slb-server/admin.json --output json
```

网页用 `login create --config ...` 生成短期登录凭证。Owner 在「连接设置 → 存储诊断」读取 SQLite 版本、消息数、附件数、DB/WAL/附件字节数；Member 无权读取全局存储统计。统计不等于完整磁盘或硬件检测。

维护操作需先停止 Server：

```bash
# 数据库结构/计数 + 每个有效附件 SHA-256；发现缺失/损坏时非零退出。
./seven-air-link storage check --data ./.slb-server --hash-files

# 默认仅预览。min-age 作用于无引用附件和上传临时文件。
./seven-air-link storage gc --data ./.slb-server --min-age 24h
./seven-air-link storage gc --data ./.slb-server --min-age 24h --apply

# 新备份与复核，不覆盖已有备份。
./seven-air-link storage backup --data ./.slb-server --out ./backup-m4-001
./seven-air-link storage verify --from ./backup-m4-001

# 仅恢复到全新目录。使用原端口运行可继续复用原 profile 的 Server 地址。
./seven-air-link storage restore --from ./backup-m4-001 --data ./.slb-restored
./seven-air-link serve --data ./.slb-restored --port 7777
```

备份包含私钥、Owner 配置、消息和附件，**必须按敏感数据保护**。SHA-256 清单用于完整性核对，不是备份作者的签名；只能恢复可信来源的备份。本版不加密备份。

回滚到 M3：停止新版，使用 M4 的 `storage restore` 将 `backup-before-m4` 恢复到新目录，再用原 M3 程序启动。**这是时间点回滚**：备份之后的消息/文件/撤销不会被带回；旧授权也可能被恢复，需要重新评估并撤销。不能用旧 M3 直接打开升级后的目录。

## 5. 两节点与 Bot 的原有操作

身份/消息/文件/服务命令保持兼容，详见 [M3 手册](docs/README-M3.md) 和 [M2 手册](docs/README-M2.md)。例如：

```bash
./seven-air-link pairing create --config ./.slb-server/admin.json
./seven-air-link join https://SERVER_IP:7777 --fingerprint SERVER_SHA256 --name Node-A --config node.json
./seven-air-link node start --config node.json

# 在共享侧本机，Node 已在线；只授予明确的来源设备/身份。
./seven-air-link share http --name backend --target 127.0.0.1:8080 \
  --allow-devices SOURCE_DEVICE_ID --config node.json
# 在访问侧：
./seven-air-link proxy open backend --listen 127.0.0.1:18080 --config source-node.json
```

Human/Bot 使用自己的配置收发消息；关联设备不自动给予网络权限。`accepted` 仅表示事务已提交，不是 Agent 已执行。Bridge 仍不运行模型、默认远程 Shell 或通用代理。

## 6. 存储边界

```text
.slb-server/
  bridge.db                    # SQLite schema 4
  bridge.db-wal / bridge.db-shm # 运行期 WAL/共享内存；不能随意删除
  metadata.json                # 从 JSON 升级后的旧版写入保护标记
  metadata.json.pre-sqlite.bak # 迁移前原始 JSON（仅升级时产生）
  uploads/file_...             # 附件字节，随机键
  server.crt / server.key / admin.json / server.lock
```

不要在服务运行时仅复制 `bridge.db` 当作备份，也不要让多台机器共享网络盘上的 SQLite 文件。程序使用单 Server 权威存储，数据库事务不能与附件文件的创建/删除组成同一原子事务；GC 负责后续回收已确认安全的残留。

消息仍最多 10,000 条/正文合计 64 MiB；附件单个 200 MiB、有效总计 1 GiB、元数据最多 4,096 条；活动最近 2,000 条。超过消息容量会拒绝新写入，不静默删除历史。没有自动历史清理或全文搜索。GC 不删除历史行，删除附件元数据仍计入既有限额。

## 7. 复跑验收

Go/C 开发工具链 + Python 3；独立 WebSocket 互通用测试专用 `websockets`；UI 用 Playwright/Chromium。它们都不是应用运行依赖。

```bash
python3 -m pip install -r scripts/requirements-test.txt
./scripts/test.sh                        # M1/M2/M3、Go/race/vet/覆盖率
./scripts/build.sh                       # 本机目标，不自动下载工具链
python3 scripts/migration_m4.py --old-binary /path/to/published-M3-binary
./scripts/test_two_linux.sh --soak-seconds 120

# 正常允许本地网络导航的浏览器环境中补跑：
python3 scripts/ui_m3_test.py --chromium /path/to/chromium
# 合成数据、零网络的 DOM 检查；不是上面的替代品：
python3 scripts/ui_m3_render_test.py --chromium /path/to/chromium
```

双环境脚本在允许的 Linux 用户/网络命名空间中创建临时 veth，使用 `10.231.0.11`/`.12` 与独立回环；不改宿主网卡/路由，结束会清理。需要 `unshare`、`nsenter`、`iproute2` 和允许的 namespace 能力。环境拒绝时标为 BLOCKED，不绕过管理策略。

CI 配置已更新，但没有在 GitHub 实际触发。短时负载/SIGKILL 测试不是断电硬件实验或长期 SLA。最终结果、失败迭代和未验收项详见 [ACCEPTANCE](docs/ACCEPTANCE.md)。
