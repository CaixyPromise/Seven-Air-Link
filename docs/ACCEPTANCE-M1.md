# M0 + M1 开发与本地闭环验收记录

版本：0.1.0-m1；日期：2026-10-28。结论：**M0 工程基线与 M1 本地连接闭环完成；可交付开发试用。目标机和真实浏览器验收仍有未完成项，不等于整个产品 MVP 完工。**

## 环境与方法

实际执行环境：Linux amd64、Go 1.23.2、Python 3.13.5、Node.js 22.16.0。应用采用 Go 标准库，`CGO_ENABLED=0` 离线构建；竞态测试单独启用其所需 C 工具链。程序运行不依赖 Python/Node.js。

黑盒环境是**一个真实 Server 进程 + 两个独立 Node 进程**，独立配对、独立凭证与配置，通过真实 HTTPS/WSS loopback socket 通信；不是直接调用 mock 网络函数。它仍不等于两台实体机器，更不等于 macOS/Windows 原生执行。

## 自动化结果

| 检查 | 实际结果 | 证据 |
|---|---|---|
| Go 单测/组件集成 | 33 个顶层 Test；总日志 49 个 RUN 条目含子测试和 fuzz 种子，全部通过 | `reports/unit-tests.log` |
| Go 竞态检测 | 通过，未报告 race | `reports/race-tests.log` |
| 测试覆盖率 | 全工程语句覆盖率 59.7%；不包含独立进程 E2E 的覆盖插桩 | `reports/coverage-summary.txt` |
| 静态检查 | `go vet ./...` 退出 0，无输出 | `reports/vet.log` |
| 帧解析短时 fuzz | 5 秒、143,108 次执行，未发现该轮崩溃 | `reports/fuzz.log` |
| 独立进程 E2E | 13 场景通过 | `reports/e2e.json` / `.log` |
| 独立 WS 互通 | Python websockets 16.0 客户端与 Go Server、Go Node 与 Python Server 双向通过 | `reports/interop.json` |
| 旧版盘点 | 3 项只读与安全边界测试通过 | `reports/legacy-tests.log` |
| 跨平台构建 | linux amd64/arm64、darwin amd64/arm64、windows amd64 共 5 个通过 | `reports/build.log` / `builds.json` |
| 离线 DOM/视觉 | 桌面/390px 布局、转义、导航、弹窗、移动端退出入口通过 | `reports/ui-offline.json` |
| 真实 Browser E2E | **BLOCKED**：Chromium 导航被管理策略拒绝；未修改策略 | `reports/ui-test.json` / `.log` |

覆盖率不是质量评分；尤其 CLI 的真实进程验收不计入 Go 单进程 coverage。fuzz 是短时补充，不能替代长期模糊测试与安全审计。

## 13 个黑盒场景

1. Server 自动初始化、TLS 启动、health/ready 可用。
2. 两个节点分别配对并通过真实 WSS 保持在线。
3. CLI `status` / `doctor` 返回正确状态和检查项。
4. 同目录第二个 Server、同配置第二个 Node 被跨进程文件锁拒绝。
5. 已使用的配对码重放失败，不增设备。
6. Device 无 Owner 管理权限，不能看到另一设备。
7. 错误证书指纹在发起有效配对前被拒绝，不写 Node 配置。
8. 暂停一个 Node，默认 5 秒心跳/15 秒超时下识别离线；另一 Node 不受影响，恢复进程后自动重连。
9. SIGKILL Server 后用同目录重启，OS 释放锁；设备 ID/授权保持，两节点自动重连，无磁盘“假在线”。
10. 活动记录不含 Owner/Device 令牌与配对码。
11. CLI SSE watch 收到重命名事件并更新显式游标文件。
12. 撤销一个在线 Node，连接关闭，进程非零退出；旧凭证再连接失败，另一 Node 正常。
13. Node / Server 正常关闭并回收进程资源。

## 本轮实测时间（单次、同机，不是性能承诺）

| 场景 | 本轮结果 | 计时定义 |
|---|---:|---|
| 暂停 Node 到服务端显示离线 | 14.933 秒 | 从 SIGSTOP 到轮询看到 offline；默认配置 |
| Server 重启到两 Node 恢复 | 0.855 秒 | 从新 Server 进程启动到在线数恢复为 2 |
| 撤销到 Node 退出 | 0.106 秒 | 从 CLI 撤销前到 Node 进程退出 |

没有进行真实 LAN 时延分位数、带宽测试，也没有 M2 数据面吞吐/并发承诺。

## 安全与失败路径覆盖

证书 pin/有效期/用途；Owner/Device 权限隔离；短期邀请过期与并发单次消费；设备撤销持久化；Host/Origin/CSRF；Cookie 登录/退出；JSON 大小与未知字段；异常 WebSocket 帧/掩码/碎片/UTF-8；静默连接超时；磁盘提交失败时授权不先改变；损坏存储不静默清空；事件游标过期；HTML 名称按文本显示；CLI 错误 JSON 不被诊断污染。

这是测试覆盖说明，不是“发现了所有漏洞”的保证。TLS/协议边界和 Windows ACL 等剩余风险见 ADR。

## 未验收项与补测操作

- Mac/Windows 真机：使用 README 建立一台 Server 和两个 Node，检查 IP、重连、撤销、退出码；验证系统防火墙、睡眠/唤醒、证书信任和凭证目录 ACL。
- 真实浏览器：在允许访问自己本地测试 Server 的环境运行 `python3 scripts/ui_test.py --chromium PATH`。该脚本覆盖网页登录、真实 SSE、网页配对后 CLI 加入、重命名/XSS、移动端、网页撤销、退出。
- 发布门禁：受支持 Go 工具链重建、漏洞扫描、协议安全评审、签名/公证策略。
- M2/M3：没有端口转发/代理/Bot/聊天/文件接口功能可验收；不把原 Python 运行能力算作 Go 已迁移。

## 页面预览的证据边界

`ui-desktop-offline.png`、`ui-mobile-offline.png` 来自真实 Chromium 渲染，但设备数据是显式标注的合成夹具，网络请求数为 0。它们证明布局和部分 DOM 交互，不证明网络登录/SSE 成功。真实连接证据以 CLI/API/进程测试为准。

## 重跑

```bash
./scripts/test.sh
./scripts/build.sh
python3 scripts/interop_test.py
go test ./internal/controlws -run '^$' -fuzz FuzzReadText -fuzztime=30s -parallel=2
```

interop 需要额外的测试用 Python websockets；UI 需要 Playwright/Chromium，均不是应用运行依赖。输出中的耗时与 fuzz 次数会随环境和重跑改变，以对应原始日志为准。
