# M4 架构决策与发布阻塞项

版本0.1.0-rc.1。M4交付专注Linux本地功能验证，保持Human/Bot普通身份与CLI/API边界，不引入Agent运行时。

## ADR-01 真正SQLite，保留业务契约

使用原生SQLite C引擎，项目内窄cgo绑定负责参数绑定、结果读取、事务与连接串行化；不是自研SQL引擎，也不启动外部Python/sqlite3进程。SQL不作为网络API暴露，extension loading关闭、trusted_schema关闭。Store资源接口和身份/网络协议保持，新增消息/事件索引表，授权资源分行差量更新。消息与审计事务提交、唯一幂等约束、revision过期写者保护均有测试。

当前仅一个应用数据库连接，Server/离线维护用同一个跨进程锁；不进行多Server共享SQLite或网络盘多机写。单连接+WAL不等于已获得多写者吞吐优化，优先保证语义正确。容量策略保留。

## ADR-02 构建取舍必须暴露

环境无法通过常规下载取得并核验Go SQLite模块与新版编译器。本版使用系统已安装的SQLite开发包；依赖版本/静态库SHA记录在reports/dependency-provenance.json。Linux静态链接SQLite，仍动态依赖libc/libm/loader。

**代价：CGO_ENABLED=1，放弃本轮无C工具链的五平台交叉编译。只交付实际构建与测试的Linux amd64程序。**Mac/Windows原生构建、SQLite链接方式、权限实现及目标机验收仍需完成。不得将M3的其他平台二进制重新标记为M4。后续可评审可信纯Go SQLite或成熟cgo驱动，不能因为当前下载限制断言自维护绑定是长期最佳方案。

## ADR-03 维护默认保守

默认GC只预览；仅清理可证明无有效引用的文件或已删除记录残留，不递归跟随路径。备份/恢复不做覆盖式操作，停服+锁限定一致性窗口。备份清单做完整性检查，不是来源认证；敏感数据的安全存放由操作者负责。回滚采用完整快照，不能无损合并升级后写入和授权撤销。

## ADR-04 两个Linux环境的证据边界

测试使用user/mount/network namespace，在临时namespace中建veth；A=10.231.0.11，B=10.231.0.12，独立localhost，分开HOME/config和真实进程。目标同为127.0.0.1:8080且返回不同环境标记；只经Bridge访问对方，确认目标未监听LAN。测试中直接关闭B的veth验证真实网络断开。

它们共享内核/宿主文件系统，不是VM/双物理机，也不能替代Windows/macOS防火墙、休眠、系统证书和ACL测试。最终有界连续负载2分钟，不是7×24稳定性证明。

## ADR-05 发布门禁：未关闭，不用测试数量替代

| 项目 | 状态与结论 |
|---|---|
| SQLite功能与M3迁移 | 本地已实现；运行证据见验收报告 |
| Go工具链 | 实际Go1.23.2，已经不在官方当前支持窗口；正式发布前换受支持版本重建/回归 |
| SQLite引擎 | 实际OS包3.46.1-7+deb13u1，需核验发行版补丁来源并升级，不作“无漏洞”保证 |
| WAL-reset历史问题 | SQLite官方说明3.51.3及以后（部分老版本有回补）修复；触发涉及同文件多连接并发写/检查点。本应用单连接且维护停服，限制了该路径，但不是已修复引擎或替代升级的证据 |
| 漏洞扫描 | 未运行在线数据库驱动的完整扫描，不伪报通过；包含依赖清单与实际版本 |
| WebSocket | 延续受限实现；回归/独立互通通过不等于成熟库替换或独立安全审计 |
| 浏览器 | Chromium联网被ERR_BLOCKED_BY_ADMINISTRATOR阻止；不改策略；离线DOM与真实API验证分别记录 |
| Mac/Windows | 无M4原生运行/发布程序；签名、公证、Windows ACL和休眠唤醒待验证 |
| 长期稳定性/磁盘故障 | 有SIGKILL、SQL事务失败注入、损坏文件、短时负载；无硬件掉电、磁盘填满或长时间跑稳承诺 |

## 权威参考（核查日期2026-10-28）

- SQLite WAL及WAL-reset说明：https://sqlite.org/wal.html
- SQLite一致性备份VACUUM INTO：https://sqlite.org/lang_vacuum.html
- SQLite PRAGMA：https://sqlite.org/pragma.html
- SQLite版本记录：https://sqlite.org/changes.html
- Go支持策略与发布记录：https://go.dev/doc/devel/release

这些参考解释技术语义，不代表本地已经下载/安装了官网最新版本。
