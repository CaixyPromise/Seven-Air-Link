# M4 开发与双 Linux 环境验收

**版本0.1.0-rc.1 · 本地验收记录。结论：M4 SQLite/迁移备份/恢复/清理/诊断及Linux双节点功能闭环通过；原计划的Mac/Windows真机、真实浏览器联网和生产发布门禁仍未关闭。**

## 1. 实现范围

实际主存储为schema4 SQLite WAL，不是JSON BLOB包装或数据库外部进程。消息/幂等索引/审计同事务，SQL序号分页；身份、服务、文件元数据分行更新。新增离线维护命令和Owner在线统计，原M1-M3网络/身份/消息协议保持。

迁移保持原始JSON字节备份/源hash，验证新DB再发布；旧目录写保护；完整Server备份、哈希校验、恢复到新目录；实际旧M3程序时间点回滚。清理preview/apply分离且保护有效附件，存储错误可诊断。

## 2. 实际环境

Linux amd64；Go1.23.2；OS SQLite3.46.1，Debian `libsqlite3-dev 3.46.1-7+deb13u1`；cgo=1，SQLite静态链接，Linux libc/libm动态链接。构建/依赖来源及静态库hash在 `reports/dependency-provenance.json` 与 `reports/build.log`。Python测试工具不是应用运行依赖。

本轮只构建Linux amd64，没有M4五平台交叉编译或Mac/Windows原生运行。工具链和引擎较旧，不将当前编译环境视为生产推荐。SQLite官方WAL-reset修复说明与发布阻塞细节在ADR-M4.md。

## 3. 两个环境是什么

```text
临时 user/mount namespace（不修改宿主路由）
  Linux环境A / net namespace A                Linux环境B / net namespace B
  10.231.0.11/24   <--------- veth --------->  10.231.0.12/24
  独立localhost / HOME / 配置                 独立localhost / HOME / 配置
  Server + Node A + Bot A + 映射              Node B + Bot B
  127.0.0.1:8080 → 返回“A”                    127.0.0.1:8080 → 返回“B”
```

两个真实网络栈、不同IP、独立回环及真实HTTPS/WSS/TCP进程；A的映射返回B而不是误连A，B的8080不对LAN监听。它们共享宿主内核和文件系统，不是两个虚拟机或物理电脑。`reports/two-linux-environments.json` 保存实际namespace inode/IP与场景结果，脚本结束清理进程及临时网络。

## 4. 最终结果

| 验证 | 最终结果 | 原始证据 |
|---|---|---|
| Go顶层测试 | **80个通过**，子测试不另算 | unit-tests.log、test-inventory.txt |
| Go竞态/静态 | race、vet通过；gofmt无差异 | race-tests.log、vet.log |
| 语句覆盖率 | **57.4%**，不包含独立进程E2E插桩 | coverage-summary.txt |
| M1回归 | **13项通过** | e2e.json / e2e.log |
| M2回归 | **18项通过**，含慢消费者、TLS透传、SSE、WS Upgrade、20/50并发、撤销/恢复 | m2-e2e.json / .log |
| M3回归 | **16项通过**，含私信隔离、幂等、文件、SSE、轮换撤销与Skill | m3-e2e.json / .log |
| 双Linux网络环境 | **14项通过** | two-linux-environments.json / .log |
| 实际M3升级/恢复/回滚 | **10项通过** | migration-m4.json、migration-final.log |
| 独立控制WS互通 | Python websockets16.0与Go双向通过；独立数据通道另在M2测试中 | interop.json |
| 消息验证器短时fuzz | 5秒配置，98,969次，该轮未发现崩溃 | fuzz-message-m4.log |
| 控制/数据帧短时fuzz | 分别116,882/125,398次，该轮未发现崩溃 | fuzz-control-m4.log、fuzz-data-m4.log |
| 旧Python只读盘点 | 3项通过，不是历史导入 | legacy-tests.log |
| Chromium离线DOM | **12项通过**，合成数据、0网络；包含Owner存储面板与身份清理 | ui-m3-offline.json、ui-m4-offline-debug.log |
| Chromium真实联网 | **BLOCKED**，ERR_BLOCKED_BY_ADMINISTRATOR；未修改策略 | ui-m3-live.json、ui-live-final.log |
| 原生程序构建 | Linux amd64通过，其他平台未构建 | builds.json / build.log |
| 源码包解压复验 | 见发布时生成的 source-package-verification.json 与外部verification清单 | source-build.log / source-tests.log |

所有状态只限上述方法。短时fuzz、单轮资源统计、覆盖率均不代表安全认证、24小时稳定性或物理LAN的时延SLA。GitHub CI配置随源码提供，没有实际触发云端CI。

## 5. 双环境14项闭环

1. 两个独立网络栈、不同IP、独立HOME/profile，Server观察到B的真实源IP。
2. 相同localhost:8080不冲突，A映射访问B，目标端口不监听LAN。
3. 独立HTTPS代理来源经目标数据通道到B。
4. Bot独立身份与显式服务ACL；双向中文私信，Owner不能经API读第三方正文。
5. 跨IP实时SSE、独立游标、incoming-only。
6. 2 MiB+附件跨配置下载，SHA-256一致。
7. 20并发512 KiB随机字节流，EOF/半关闭与摘要正确。
8. 50并发同类校验，数据不串流。
9. 20持续连接跨心跳运行120秒目标，60轮收发/状态检查。
10. 关闭B真实veth接口；B离线、A不受影响；恢复后同一映射端口可用；旧流终止。
11. Server SIGKILL、SQLite恢复、已确认消息和幂等/附件保留，映射租约重建。
12. Bot撤销关闭存量流与源监听，不撤销Node。
13. B设备撤销，远端Node退出、关联Bot失效，A保持授权。
14. 停服后SQLite完整性与有效附件逐个hash校验通过。

### 最终单轮同机隔离网络测量

| 场景 | 实测 |
|---|---|
| 20并发，每流512 KiB | 0.173秒 |
| 50并发，每流512 KiB | 1.154秒 |
| 20持续连接 | 122.823秒含收尾、60轮 |
| veth断链到离线 | 14.125秒，默认5s心跳/15s静默判定 |
| Server重启到HTTP映射/历史/附件恢复 | 2.754秒 |
| 持续负载前/后Server RSS | 20,934,656 / 20,000,768字节 |
| 同一阶段Server FD | 21 / 17 |

资源快照下降不能推导不存在泄漏。独立Bot映射有自己的3秒租约续订周期，不能把某一HTTP映射恢复当作其他映射同时已恢复；测试分别等待各映射的有界恢复。恢复不重发旧TCP请求字节。

## 6. 迁移与恢复的真实范围

旧程序为之前发布M3 Linux二进制，SHA-256：
`aaf29b38e179fe98525e64c6bf8b8c23e1b5e2b9eb982b9f3844fab22bdcef10`。

先由它创建Node/Bot、UTF-8私信、回复链、附件与真实TCP服务；M4停服备份旧目录，迁移schema3到SQL4，验证旧配置/Service ID/ACL/本机批准/消息/附件/幂等和私信授权。另执行SIGKILL、拒绝运行中维护、SQL全量校验、VACUUM独立备份、篡改拒绝、全新目录恢复并实际运行消息/网络、GC幂等与引用保护。最后将升级前备份恢复，由原M3程序真实运行；验证升级后写入不在旧时间点备份中。

这不是将原Python匿名记录导入新身份，也不是零损失反向合并升级后数据。回滚会恢复备份中的授权，必须评估旧撤销是否需要重做。

## 7. 额外错误路径与加固

Go测试包含：SQLite参数绑定/事务回滚、消息+审计注入失败不部分提交、唯一约束/分页、过期revision写者拒绝、迁移备份精确性、已封印pending恢复、损坏源不重置、备份路径穿越/软链接/软链接祖先拒绝、完整恢复/篡改、有效附件/新partial保护、缺失/hash损坏诊断、私有目录/配置软链接、Owner-only存储API与真实HTTPS静态脚本路由。

M1/M2/M3既有权限/协议/失败测试持续通过。新增小型SQLite绑定仍需要独立审查；不把通过测试误写为第三方安全认证。

## 8. 失败迭代与证据诚实性

早期运行有工具时间预算中断，随后独立及完整M2重跑通过。双环境脚本首次使用嵌套`ip netns exec`触发额外sysfs挂载限制，改为只进入已建立net namespace的`nsenter`；未修改主机或浏览器管理策略。测试中另修正了“一个映射恢复即所有映射恢复”的错误假设，以及关联设备撤销应返回已有契约`principal_inactive`而非`unauthenticated`的预期。最终完整重跑14项通过，旧失败日志保留于iterations-m4，未从失败中挑选局部通过拼接总结果。

离线UI截图明确标记合成数据；浏览器联网受阻仍保留BLOCKED，不用截图或HTTP API通过替代它。上线时需重新验证实际系统剪贴板和跨端口代理信任。

## 9. 未关闭的退出条件

Mac/Windows原生构建运行及ACL/睡眠/防火墙、真实浏览器联网、受支持Go与SQLite补丁核验/漏洞扫描、WebSocket成熟替换或独立审计、签名/公证、长时稳定性与磁盘/硬件故障，均不在PASS范围。详见ADR-M4.md、TARGET-ACCEPTANCE-M4.md。

本交付可用作下一步开发与Linux功能验收基线，不应称作M4全部原始发布标准已满足。
