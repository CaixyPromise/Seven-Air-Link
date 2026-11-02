# 身份边界修复：本地实现与验收记录

**版本：0.1.0-rc.4（未发布）｜本轮完成日期：2026-11-02（UTC+8）**

## 结论

已实现并本地验证：单 Server 权威、独立 Human/Client 接入、仅 Server 本机
Owner 管理、显式 Browser/CLI 绑定、按凭证撤销、成员自有 Bot/Node、便签来源
和权限驱动的界面。旧聊天、文件、网络转发、便签自动保存/CAS 不移除。

**没有推送新代码或发布新 Tag/Release。**当前可用 GitHub 连接只暴露读取工具，
远端 `fix/client-identity-boundaries` 仍为 `d33b84886b3ff52fa1dd3132943aa2a0345a6db7`。
这不是账号没有仓库权限的结论，也不是已经触发云端编译的声明。

基线来自该提交的 Actions source artifact（run 41326804025 / artifact 13091023765）。
源码快照 ZIP SHA-256：`60ae1ae3d45c1a18e60eaa40b5b73d75039b04df869bcca18eef05169afe19c2`。
交付补丁以此树为基线，不重写 rc.1/rc.2/rc.3 标签。

## 实际环境

Linux amd64、Go 1.23.2、C 编译器、系统 SQLite；程序使用 cgo。
测试使用 Python、websockets、Playwright 与 Chromium；它们不是运行依赖。

双环境使用 Linux 用户/挂载/网络命名空间及 veth：A 为 `10.231.0.11`，
B 为 `10.231.0.12`。各有独立的 localhost、HOME、客户端配置和进程，
但共享宿主内核与文件系统；不是两台物理机器，也不是 Mac/Windows。
只有 A 运行一个 Bridge Server；B 不执行 `serve`。

## 本轮最终结果

| 检查 | 结果 | 原始证据（QA ZIP 中 reports/） |
|---|---|---|
| Go 顶层测试 | 110 个通过；不重复统计子测试 | unit-tests.log |
| gofmt / go vet | 通过 | vet.log / 完整回归退出 0 |
| go test -race | 通过，未报告竞态 | race-tests.log |
| 语句覆盖率 | 57.4%，不含独立进程 E2E 插桩 | coverage-summary.txt |
| 连接、网络、协同原有回归 | 13 / 18 / 16 项通过 | e2e.json / m2-e2e.json / m3-e2e.json |
| 身份真实进程验收 | 11 项通过 | identity-e2e.json |
| 两个 Linux 网络环境 | 12 项通过 | identity-two-linux.json |
| 实际 rc.3→rc.4 升级、恢复、回滚 | 7 项通过 | identity-migration.json |
| PTY 密码不回显 | 通过 | rc3-pty.json |
| 独立 WebSocket 控制互通 | 2 项通过 | interop.json |
| 离线界面/交互 | 10 项通过，零网络、合成数据 | identity-ui-offline.json |
| 真实 Chromium 联网 | BLOCKED，ERR_BLOCKED_BY_ADMINISTRATOR | identity-ui-live.json |
| 本机构建 | Linux amd64 成功 | builds.json / build.log |
| 新版 Mac/Windows 原生构建 | 未执行，不能借用 rc.3 结果 | 待 GitHub CI |

完整 `scripts/test.sh` 最终退出码为 0。最终 UI 异步隔离补丁之后重新构建程序，
再运行身份进程、双网络环境、真实旧版迁移、PTY 和离线界面测试。
Go 后端源文件未在最终 race 之后修改。源码包解压构建、测试与二进制比对结果
另见 `SOURCE-VERIFICATION.json`，不把尚未执行的步骤写成通过。

## 八项用户场景对应证据

| 场景 | 验证内容 |
|---|---|
| A 启动 Server，B 浏览器访问 | 以实际 HTTPS Cookie 客户端执行密码登录，得到 Member；Owner 只能本机凭证进入 |
| 同一个加入密码 | 相同名称、密码和 IP 得到不同 Principal/Client；记住自己的 Cookie 才恢复自身身份 |
| 普通客户端直调管理 API | 密码/全局身份/设备/存储等请求拒绝；伪造来源头无法提权 |
| B CLI 连接 A | pin 验证后在 B 生成自己的配置；无 Owner 复制，无第二个 Server |
| B 本地端口映射 | A/B 的回环目标同端口返回不同标记；B 映射返回 A 标记，A 没有该源监听 |
| Browser / CLI 未绑定 | 相同 IP 不关联；两分钟一次性码显式关联同 Principal，但凭证不同 |
| 定向撤销 | 实际存量 TCP 流和前台监听关闭；同身份另一 CLI、Browser、Node 与其他成员仍正常 |
| 共享便签 | CAS 一次只成功一份写入，记录 Actor/Client/Session/IP；崩溃重启后版本、内容、来源保留 |

“浏览器访问”的网络授权逻辑由真实 HTTPS/Cookie 请求及 API 测试验证；
浏览器 DOM 的角色显示由离线夹具验证。两者不能拼成“真实浏览器联网已通过”。
提供 `ui_identity_test.py` 供允许联网的 CI 环境完成这项独立门禁。

## 关键额外检查

- 远程持有测试用 Owner token 仍被 `owner_local_only` 拒绝，X-Forwarded-For 不能绕过。
- 回环请求没有凭证不会获得 Owner；本机用加入密码也只是 Member。
- 一次性 CLI 绑定并发消费、发行凭证撤销后的邀请消费、Node 配额在领取时重检。
- Human 只管理自己的 Bot；Bot 不再创建身份，不继承服务 ACL。
- 单 Client 撤销与完整 Principal/Device 撤销语义分开；并非“全空间退出”。
- UI 隐藏管理入口之外，后端独立鉴权。退出后延迟返回的邀请/配对码不会重新弹出。
- 旧 rc.3 共享管理员密码在 schema 5→6 中禁用；不是自动转换成加入密码。
- 实际用旧 rc.3 程序建数据，升级保留身份/设备/消息/便签，再用旧备份和旧程序完成回滚。

## 尚未关闭的边界

新版本 GitHub 提交、五平台 CI、真实浏览器联网、Mac↔Windows 实体机、
系统文件 ACL、签名/公证、安全审计尚未完成。Go 1.23.2 仅是容器实际工具链，
不是正式发布工具链推荐；云端 workflow 使用 supported/stable 工具链。

服务端本机范围是直连 B/S 下的 socket loopback + 私有 Owner credential，
不是远端硬件证明。不应通过不可信本机代理再次转发管理入口。
同一加入密码仍有效时，撤销者可从新客户端重新申请加入；需换/关闭加入密码
阻止新加入，不用 IP 当硬件封禁。数据库与备份没有应用级加密。

## 复跑

```bash
bash scripts/test.sh
bash scripts/test_identity_linux.sh --binary "$PWD/bin/seven-air-link"
python3 scripts/migration_identity.py --old-binary /path/to/rc3/seven-air-link --new-binary bin/seven-air-link
python3 scripts/ui_identity_test.py --binary bin/seven-air-link --chromium /path/to/chromium
python3 scripts/ui_identity_render_test.py --chromium /path/to/chromium
```
