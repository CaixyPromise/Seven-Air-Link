# M3 开发与本地闭环验收报告

版本：**0.3.0-m3**；日期：2026-10-28。结论：本轮要求的 **CLI + Skill、Bot 消息收发、操作事件和 Web 协同界面已实现**；Linux 本地代码/网络验收通过。原设计中 SQLite 和旧 Python 数据导入仍未完成，不将整个存储迁移里程碑宣称完工。

## 1. 实际环境与方法

Linux amd64、Go 1.23.2、Python 3.13.5、Node.js 22.16.0。应用为无第三方 Go 模块的单二进制，运行不依赖 Python/Node.js；测试使用 Python websockets、Playwright/Chromium 等开发工具。发布采用 CGO_ENABLED=0、trimpath、buildvcs=false；竞态测试启用所需 C 工具链。

M3 黑盒环境是独立 Server、两个独立 Node、多个 CLI/端口映射进程和临时业务服务，通过真实 HTTPS/SSE/WSS/TCP loopback 通信。Bot 是真实独立凭证的身份，不是实际调用的 AI 模型。测试没有假设 Claude/Codex/Seven Agent 已自动执行消息，也没有把同 Linux 模拟说成 Mac↔Windows 真机验收。

## 2. 自动化测试结果

| 检查 | 实际结果 | 原始证据 |
|---|---|---|
| Go 单元/组件测试 | 63 个顶层 Test 全部通过；包含子测试和 fuzz 种子时 RUN 数量更多，不混为独立场景 | reports/unit-tests.log |
| 竞态检查 | go test -race 全部通过，未报告数据竞争 | reports/race-tests.log |
| 静态检查/格式 | go vet 成功；gofmt 无未格式化文件 | reports/vet.log；scripts/test.sh |
| 语句覆盖率 | 合并 55.3%，不包含独立进程 E2E 插桩 | reports/coverage-summary.txt |
| M1 兼容回归 | 13 项真实进程场景通过 | reports/e2e.json |
| M2 网络回归 | 18 项场景通过，含 20/50 并发流、半关闭、HTTP/SSE/WS、TLS 透传和故障路径 | reports/m2-e2e.json |
| M3 协同黑盒 | 16 项场景通过 | reports/m3-e2e.json |
| M2→M3 升级 | 用实际发布的 M2 程序建库与转发，再原地升级，7 项检查通过 | reports/migration-m3.json |
| 独立 WS 互通 | Python 独立实现与 Go 控制端双向通过；数据协议互通另见 M2 | reports/interop.json |
| 消息验证器短时 fuzz | 配置 5 秒，本轮约 6 秒收尾、123,333 次执行，未发现该轮崩溃 | reports/fuzz-m3.log |
| 旧数据只读盘点 | 原有 3 项通过；不等于导入完成 | reports/legacy-tests.log |
| 五平台编译 | linux amd64/arm64、darwin amd64/arm64、windows amd64 全部成功 | reports/builds.json；reports/build.log |
| 离线 Web | 10 项布局/DOM 检查通过，0 网络请求，明确合成数据 | reports/ui-m3-offline.json |
| 真实浏览器联网 | **BLOCKED**：ERR_BLOCKED_BY_ADMINISTRATOR；没有绕过管理策略 | reports/ui-m3-live.json |

短时 fuzz、单轮进程测试和 coverage 都不是生产安全认证或持续稳定性证明。CI 配置随源码提供，但本次没有在 GitHub 执行 CI。各平台程序未签名/公证。

## 3. M3 的 16 项真实进程检查

1. 两个 Node、Human/Bot 独立 profile 与统一参与者发现。
2. Member 不能创建设备/身份管理授权，Device 不能冒充聊天主体。
3. 中文 Bot 私信和回复；第三方 Human、Owner 均不能通过 API 读取正文。
4. 发送幂等、同 key 不同内容冲突、CLI 参数值保真和超限拒绝。
5. 共享频道、历史分页、离线持久接受；不宣称模型执行。
6. CLI NDJSON 重放/续读；incoming-only 跳过自身发送；游标身份不符明确拒绝。
7. 实际 HTTPS SSE 收到另一 CLI 提交的新消息。
8. 收件箱不可达返回非零错误，不伪装为空消息成功。
9. 超过 1 MiB 的二进制附件往返、SHA-256 一致、禁止覆盖和扩大私信附件受众。
10. actor/correlation 审计过滤与 Member 范围；正文和令牌不进入活动记录。
11. SIGKILL Server 后同目录重启，身份、消息、文件字节、Node 授权保持。
12. 设备关联不隐式授权服务；显式 Principal ACL 后 Bot 能走真实 TCP 隧道。
13. 轮换凭证使旧配置、租约和流失效；新凭证保持身份 ID，但旧能力不复活。
14. 撤销 Bot 关闭已有流和前台映射，不撤销无关 Node。
15. 撤销关联设备使 Bot 凭证与收件订阅失效。
16. 程序内嵌 Skill 与可运行命令一致，不依赖模型厂商协议。

最后一轮同机记录：CLI SSE 新消息到达约 **0.195 秒**；关联设备撤销到收件订阅关闭约 **0.194 秒**。只是本轮样本，不是 LAN 延迟 SLA 或分位数承诺，以 JSON 中 elapsedSeconds 为准。

## 4. 真正测试的升级范围

使用原交付 M2 Linux 程序（SHA-256 `0b4fed0bd98eef92bca7bc5753820398abea7ef0c5bfae45f2199f7e491145a4`）启动 Server、配对两个设备、批准服务、完成 TCP 请求。停止后用 M3 原目录重启，检查 v2 备份/schema 3、原证书和凭证、Service ID/ACL/本机批准、设备重连、TCP 可用，再创建 Bot 发送消息。

这证明 **M2 元数据升级**，不是原 Python state.json 历史导入，也不是把旧匿名用户认证成新的 Principal。降级需恢复升级前完整备份。

## 5. Web 证据边界

真实 Chromium 导航到本机测试 HTTPS 时被管理策略拒绝，不能把此状态报告为通过。`scripts/ui_m3_test.py` 已提供独立 Owner/Bot 浏览器会话、真实私信 SSE、角色 UI、上传/预览、移动端和撤销检查，需在允许网络导航的开发环境补跑。

离线检查直接加载真实交付 HTML/JS/CSS，但把网络替换为内存测试对象；10 项包含桌面/390px 布局、HTML 转义、会话切换/草稿、消息目标、合成粘贴/拖放事件、附件预览、邀请与清码、Principal 服务授权选择、退出/延迟响应隔离和 Member 入口。它验证实际页面逻辑，不证明浏览器网络路径或 OS 剪贴板兼容。

截图文件 `ui-collaboration-desktop-offline.png`、`ui-participants-desktop-offline.png`、`ui-collaboration-mobile-offline.png` 均明确标注离线示例。不能用于宣称真实多机正在协作。

## 6. 交付一致性

本轮验收 Linux 程序与 dist 中发布程序的 SHA-256 相同：`aaf29b38e179fe98525e64c6bf8b8c23e1b5e2b9eb982b9f3844fab22bdcef10`。

交付脚本会对源码 ZIP 干净解压后再次离线编译、运行 Go 测试、检查合同并对比上述哈希；结果存放在 `reports/source-package-verification.json`。源码清单为 `SOURCE-SHA256SUMS`（不包含报告、二进制或清单自身）；五个平台哈希在 `dist/SHA256SUMS`，最终 ZIP 哈希另行交付。

## 7. 未通过/未实现，不能省略

**未实现**：SQLite、旧 Python 历史导入、自动存储清理/垃圾回收、全文检索、自定义频道、持久 ACK/已读/任务完成、自动运行或唤醒 AI。

**未在此环境验收**：macOS/Windows 原生运行、真实跨设备 LAN/防火墙、睡眠唤醒、Windows ACL、真实浏览器联网、系统剪贴板文件对象兼容、签名/公证。

**发布门禁**：受支持工具链重建与漏洞扫描；继承的受限 WebSocket 需成熟库替换或独立协议审计；长期资源/磁盘压力、掉电恢复和存储 GC 验证。当前 JSON 全量快照且容量受限，不能用于无限消息或高吞吐生产承诺。

## 8. 复跑入口

```bash
./scripts/test.sh
./scripts/build.sh
python3 scripts/migration_m3.py --old-binary /path/to/m2/seven-air-link --new-binary bin/seven-air-link
go test ./internal/store -run '^$' -fuzz FuzzMessageValidation -fuzztime=5s -parallel=2
python3 scripts/ui_m3_test.py --chromium /path/to/chromium
python3 scripts/ui_m3_render_test.py --chromium /path/to/chromium
```

详见 [M3 CLI](CLI-M3.md)、[ADR](ADR-M3.md)、[真机验收清单](TARGET-ACCEPTANCE-M3.md) 与 [后续任务](M4-HANDOFF.md)。
