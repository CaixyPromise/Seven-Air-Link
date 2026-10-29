# M4 存储、迁移、备份与恢复操作说明

## 责任与事务

Server 仍然只有一个权威实例；所有本地维护都取得同一个 `server.lock`。SQLite 采用 WAL、FULL 同步、5 秒 busy_timeout、单连接串行事务。独立消息表拥有 `(sender_id,client_message_id)` 唯一约束，正文/附件引用/回复/关联 ID 原样保留；消息与审计/序号在同一次事务中提交。

授权等资源以分行 JSON 保存，只有变更行进行 UPSERT；不是整个状态快照写 DB。内存保留小型授权/资源目录，不缓存全部历史消息/事件。按 seq 的 keyset 查询施加身份可见性；只有显式导出才读取整个消息集合。重复消息同 key 同内容返回原结果，改变内容仍冲突。

文件字节在数据库外。数据库标记删除后物理删除失败可以由后续 GC 重试；未成功完成数据库提交的上传不会形成有效附件授权，但可能留下孤立文件。不要把 SQLite 的事务原子性宣传成文件与数据库的跨资源事务。

## 迁移状态机

1. 停服，完整备份 Server 目录及各节点本机配置。
2. 验证 legacy schema 1/2/3 的授权、序号、消息和文件元数据；损坏数据拒绝，不重置。
3. 原始字节保存到 `metadata.json.pre-sqlite.bak`，记录 SHA-256。已存在且内容不同的备份不得覆盖。
4. 创建 `bridge.db.migrating`，在事务中导入，建立索引，核对计数与每条消息序列化结果。
5. 记录迁移封印 source SHA/version，checkpoint 后关闭，写入旧 JSON version 4 保护标记。
6. 原子发布为 `bridge.db`，同步目录，再打开检查。启动新版本也会触发这条路径。

若已封印 pending DB 与原始备份 hash 一致，且 source 为原字节或匹配保护标记，重跑可继续完成发布。没有封印、备份不匹配、source 在迁移过程中改变时拒绝继续：保留所有文件用于诊断；不要手改 source 或删除 backup。可从事先完整备份恢复至新目录重试。自动升级不等于已自动生成完整可回滚备份。

M3→M4 有实际发布旧程序的闭环测试。schema1/2 的转换有 Go 兼容测试，不等于本轮再次运行了所有历史发布程序。原始 Python 匿名聊天 `state.json` 不在本次转换范围。

## 备份的一致性与保护

维护命令先确认 Server 停止并持有文件锁。SQLite 使用 `VACUUM INTO` 创建不依赖源 WAL 的独立数据库；同时复制服务端证书/私钥、Owner 配置、有效附件，以及存在的 legacy 保护标记。临时目录完成 SHA-256/大小、SQL/schema/计数、Owner令牌哈希、证书指纹/私钥匹配、每个附件 hash 验证后才发布。

清单只允许固定文件名和合法随机附件键，拒绝重复路径、`../`、绝对路径、软链接和未列明内容；输出不允许位于输入目录内（包括软链接别名路径），也不覆盖已有目录。复核只读；恢复复制到新暂存目录并再次复核后原子发布。文件与目录执行 fsync，但这些测试没有模拟硬件掉电或不遵守 fsync 的文件系统。

备份是**完整 Server 恢复集合**，不是整个设备集群备份；它不带其他机器的 Node/Bot profile 与本机服务批准文件，也不保留上传孤立垃圾。备份不是加密包或签名包；完整性清单不能抵抗有能力同时修改内容和清单的攻击者。仅从可信、访问受控的存储恢复。

恢复会回到备份中的授权状态。备份后被撤销的旧身份可能重新有效；这不是可忽略的副作用。恢复后应复核/轮换受影响的凭证，并明确备份后的消息/文件没有自动重放。

## 清理与诊断

`storage gc` 默认 preview。`--apply` 才删除；`--min-age` 默认24h，控制无索引附件和 `.upload-*` 临时文件的年龄。已持久标记删除的附件残留可立即回收。仍有效附件、新临时文件、软链接、未知文件、子目录不删除。删除失败可重试，历史元数据不抹除；没有计划任务或后台 GC 守护程序。

`storage check` 执行 SQLite quick_check、元数据/消息/事件计数与 foreign_key_check，核对所有有效附件的存在性/类型/大小；`--hash-files` 增加每个附件 SHA-256。发现问题非零退出，包含结构化 `fileIssues`；不是自动修复命令。

`doctor` / Owner `GET /api/v1/storage` 只报告当前可读取状态和统计，不执行全量附件扫描。运行中可读，但备份/迁移/GC只在停服后执行。普通 Device/Member不能读取全局存储统计。

## 命令契约

```sh
seven-air-link storage check --data DIR [--hash-files] --output json
seven-air-link storage migrate --data LEGACY_DIR --output json
seven-air-link storage backup --data DIR --out NEW_BACKUP_DIR --output json
seven-air-link storage verify --from BACKUP_DIR --output json
seven-air-link storage restore --from BACKUP_DIR --data NEW_DATA_DIR --output json
seven-air-link storage gc --data DIR [--min-age 24h] [--apply] --output json
seven-air-link storage export-json --data DIR --out NEW_JSON --output json
```

`export-json` 包含凭证哈希和私信正文，是 schema3 交换/检查文件，不是完整备份或一键降级方案；必须按敏感文件处理。所有输出路径拒绝覆盖；成功0、错误非0。所有维护命令在 Server 本机执行，不是向远端 Bot 开放的管理 API。
