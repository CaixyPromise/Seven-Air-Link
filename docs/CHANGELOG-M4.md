# 0.1.0-rc.1

- 迁移JSON主存储到SQLite WAL；消息/审计SQL表，幂等唯一约束、keyset分页、差量资源写入、事务错误回滚。
- 旧JSON原字节备份、迁移SHA封印、pending恢复、旧写者保护。
- 本地storage check/migrate/backup/verify/restore/gc/export-json命令；停服锁、清单校验、新目录恢复、GC默认预览。
- Owner存储诊断API/CLI/Web只读面板，身份切换清空统计且拒绝迟到响应。
- POSIX私有目录、数据库/凭证/锁软链接拒绝、备份路径与软链接祖先校验。
- 新双Linux网络环境测试，真实veth/IP/独立localhost、2分钟负载、链路断开/恢复、SQLite崩溃恢复和撤销。
- 实际发布M3升级、M4恢复实际运行、旧M3时间点回滚测试；保留M1-M3回归。
- 构建变为CGO_ENABLED=1原生SQLite；本轮仅Linux amd64程序，不声称跨平台验收或生产发布就绪。
