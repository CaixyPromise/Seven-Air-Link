# M4 CLI增量

原网络/身份/消息命令兼容，见CLI-M3.md、CLI-M2.md。所有命令支持 --output json；错误非零退出。

新增维护命令、数据安全注意与示例见 [STORAGE-M4.md](STORAGE-M4.md)。这些命令必须在拥有Server数据目录权限的机器上执行，不使用Bot配置来授权。

Owner `doctor --config admin.json` 返回 `storage`，包含engine/sqliteVersion/schema/messages/messageBytes/eventSeq/files/fileBytes/databaseBytes/walBytes。GET `/api/v1/storage` 为同一只读能力，非Owner拒绝。统计的integrity=not_run不能误读为完整检查已通过；完整检查使用停服的storage check。

网页「连接设置」Owner可读取存储统计；维护删除/恢复只提供CLI说明，不远程执行。

`storage gc --apply` 明确可能删除候选文件；先审阅preview并备份。`storage restore` 只写不存在的新目录，不就地覆盖。`storage migrate` 完成一次后再次调用会拒绝已存在DB，而不是二次导入。
