# M2 开发与本地闭环验收

版本：**0.2.0-m2**；日期：2026-10-28。

**结论：M2 核心网络功能已实现，Linux 同机独立进程/socket 的开发闭环通过。真实 Mac↔Windows 与浏览器联网验收未完成；本版不是生产安全认证发布。**

## 1. 实际环境与证据边界

Linux amd64，Go 1.23.2，Python 3.13.5；运行无需第三方 Go 模块或其他服务。独立互通测试使用 Python websockets 16.0；UI 使用 Chromium 144.0.7559.96。测试不是仅 mock：真正启动 Server、两台独立凭证/配置的 Node、多个前台转发进程、TCP/HTTP/TLS/WebSocket 目标，通过回环 TCP/TLS/WSS 通信。

单主机模拟不能证明跨系统防火墙、睡眠、多网卡、物理局域网性能或 Windows/macOS 原生行为。UI 离线渲染不能证明网络登录成功。

## 2. 最终测试结果

| 项目 | 结果 | 原始证据 |
|---|---|---|
| Go 单元/组件集成 | 46 个顶层 Test 通过（另有子用例和 fuzz 种子） | `reports/unit-tests.log` |
| 数据竞争检测 | `go test -race` 通过，无 race 报告 | `reports/race-tests.log` |
| 全工程语句覆盖率 | 56.4%（不含独立进程 E2E 覆盖插桩） | `reports/coverage-summary.txt` / `coverage.out` |
| 静态检查 | `go vet ./...` 通过，gofmt 检查通过 | `reports/vet.log` |
| M1 真实进程回归 | 13 个检查场景通过 | `reports/e2e.json` / `.log` |
| M2 真实进程网络验收 | 18 个检查场景通过 | `reports/m2-e2e.json` / `.log` |
| 独立控制协议互通 | Python客户端↔GoServer、GoNode↔PythonServer通过 | `reports/interop.json` |
| 独立 M2 数据协议 | Python客户端直接使用 slb.data.v1 的 READY/分片 DATA/FIN，通过 | `reports/m2-e2e.json` |
| 数据消息短时 fuzz | 5秒，92,774 次执行，未发现该轮崩溃 | `reports/fuzz-data.log` |
| 控制消息短时 fuzz | 5秒，81,512 次执行，未发现该轮崩溃 | `reports/fuzz-control.log` |
| 旧版只读盘点回归 | 3项通过 | `reports/legacy-tests.log` |
| 五个平台构建 | Linux amd64/arm64、Darwin amd64/arm64、Windows amd64通过 | `reports/builds.json` / `build.log` |
| UI 离线渲染/交互 | 桌面、390px、服务/租约、权限弹窗、纯文本转义检查通过；零网络请求 | `reports/ui-offline.json` |
| 实际浏览器网络 E2E | **BLOCKED**：ERR_BLOCKED_BY_ADMINISTRATOR；未修改或绕过策略 | `reports/ui-test.json` / `.log` |
| GitHub CI / 原生目标机 | **NOT RUN**；提供workflow与操作清单，不算已通过 | `.github/workflows/ci.yml` / `docs/TARGET-ACCEPTANCE.md` |

覆盖率不是可靠性评分；forward/node/tunnel 多数执行由 httpapi 集成测试和真实进程测试覆盖，后者不计入 Go coverage。短时 fuzz 不代表安全认证、完整协议套件或长期稳定性压测。

## 3. M2 真实进程检查明细

1. **PASS** — real Server + 2 Nodes; local-only approval and registered service readiness
2. **PASS** — local TCP mapping and UTF-8 HTTP access without exposing target listening address
3. **PASS** — 8 MiB HTTP upload over TCP mapping; SHA-256 exact
4. **PASS** — 20 concurrent TCP streams; FIN, random binary and SHA-256 isolation
5. **PASS** — 50 concurrent TCP streams; FIN, random binary and SHA-256 isolation
6. **PASS** — 20 persistent simultaneous TCP streams across heartbeats; periodic byte isolation
7. **PASS** — slow consumer backpressure preserves other flows and control heartbeat; explicit stop exits CLI
8. **PASS** — TLS-over-TCP passes through unchanged and certificate validation succeeds
9. **PASS** — isolated HTTP gateway: credential stripping, redirect rewrite and unbuffered SSE
10. **PASS** — independent Python client speaks slb.data.v1: READY, fragmented DATA and bidirectional FIN
11. **PASS** — WebSocket Upgrade through HTTP gateway; independent Python implementation; text + 1 MiB binary
12. **PASS** — unauthorized device, non-loopback target/listener and occupied listener denied
13. **PASS** — ACL edit closes existing streams and rejects new flows; explicit regrant restores access
14. **PASS** — target Node disconnect closes streams; known-offline fails within 2s; same local port recovers
15. **PASS** — Server SIGKILL/restart: authorization persistent, Nodes reconnect, CLI recreates lease at same port
16. **PASS** — stopped service tombstone survives Node restart; explicit local re-share obtains new service identity
17. **PASS** — audit contains operation metadata, not credentials or TCP payload
18. **PASS** — post-workload process resource snapshot

## 4. 本轮测量，不是 SLA

M2 进程测试为加速故障验收使用1秒心跳/4秒离线；M1回归单独验证默认5秒/15秒。以下是单主机单轮结果，不可用来宣称物理LAN吞吐或跨系统延迟。

| 项目 | 本轮数值 |
|---|---|
| 20 concurrent TCP streams; FIN, random binary and SHA-256 isolation | 0.258 秒 |
| 50 concurrent TCP streams; FIN, random binary and SHA-256 isolation | 1.155 秒 |
| 20 persistent simultaneous TCP streams across heartbeats; periodic byte isolation | 12.2 秒 |
| SSE 首条事件 | 0.038 秒 |
| Server 重启后同端口访问恢复（包含租约续期等待） | 2.763 秒 |

20/50路随机校验每流512 KiB；20条持续流跨6轮、约12秒，不能当成数小时稳定运行。慢消费、TLS-over-TCP、HTTP流式响应与WebSocketUpgrade另做测试。资源快照是描述性观察，不是内存泄漏证明。

- server：RSS 14,561,280 bytes，文件描述符 15。
- source-node：RSS 9,916,416 bytes，文件描述符 8。
- target-node：RSS 10,678,272 bytes，文件描述符 9。

## 5. 安全与恢复覆盖

显式服务级ACL；Owner管理不等于使用；本机批准与Server授权独立；数字回环限制；跨身份/重复消费数据票据拒绝；控制session绑定；浏览器Cookie不得作为数据WS凭证；目标拒绝/离线/建连超时；配额回收；FIN半关闭；源/目标设备撤销关闭流；ACL修改关闭旧流；租约超时关闭存量流；停用服务不随重连复活；Server重启只重建新连接；代理来源与管理来源分离、一次性表单票据、服务范围与revision验证、Cookie/Header过滤；正文/凭证不进入活动日志。

HTTP Gateway 的不同服务仍同源，不是敌对网页隔离。源监听默认/仅允许回环，任意本机程序仍可访问该端口；Bridge 不识别每个本机进程的 OS 用户权限，业务认证需应用自身维持。

## 6. 发布程序与复验

实际用于最终 M1/M2 进程、独立协议验收的是交付的 Linux 程序：

```text
0b4fed0bd98eef92bca7bc5753820398abea7ef0c5bfae45f2199f7e491145a4  dist/seven-air-link-linux-amd64
```

其余平台完成编译与文件格式识别，不曾在对应机器执行。详细SHA-256在 `dist/SHA256SUMS`。

源码包解压后的编译/测试/二进制比对记录见 `reports/source-package-verification.json`（该记录只在实际复验后生成）。包级SHA-256位于外部交付清单，避免自包含哈希循环。

## 7. 尚未关闭的门禁

真实Mac/Windows启动与互通、真实浏览器UI/证书/跨端口表单交互、系统睡眠/Wi-Fi切换、Windows凭证ACL、受支持Go版本重建及漏洞扫描、二进制签名/公证、数据传输实现独立审计/成熟库替换。`ADR-M2.md` 记录了当前受限自维护WS实现和不能获取常规模块下载的取舍；不以短期测试替代供应链、安全和长期负载验证。

JSON存储不是SQLite，活动最多2,000条。Bot/消息/文件/历史导入属于M3，UDP/HA/Agent Runtime不在本期范围。

## 8. 重跑

```bash
python3 -m pip install -r scripts/requirements-test.txt
./scripts/test.sh
./scripts/build.sh
python3 scripts/ui_test.py --binary dist/seven-air-link-linux-amd64 --chromium /path/to/chromium
python3 scripts/ui_render_test.py --chromium /path/to/chromium
```

`reports/iterations/` 是开发中间记录（可能含失败或执行时限中断），不混入最终通过结论。`reports/m1/` 是原M1历史证据。
