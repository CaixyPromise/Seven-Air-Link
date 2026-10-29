> 历史基线：本文件保留 M1 交接/迁移方案。M2 实现与当前验收以 `ADR-M2.md`、`ACCEPTANCE.md` 和根目录 README 为准。

# M0：旧 Python 数据盘点、保留与迁移路线

## 本次实际完成

原始 Python 版本压缩包完整复制到 `legacy/legacy-python-bridge.zip`，`legacy/SHA256SUMS` 可校验字节一致。新 Go 使用不同程序、端口和数据目录，没有在旧目录运行导入或删改。

原版数据结构：`state.json` 的 `messages/files/note`，附件 bytes 位于 `uploads/<32 位十六进制 id>`。旧版消息最多 200 条；从历史已经淘汰的数据不能由迁移工具凭空恢复。

新增 `scripts/legacy_inspect.py`，只读获取状态哈希、消息/便签计数、附件大小与 SHA-256、缺失/孤立附件、非法 ID 和符号链接。它**不是导入器**，不生成新的可信身份。测试使用合成数据，验证输入文件哈希保持不变，检查非法编码和符号链接被拒绝。

## 用户数据盘点操作

1. 停止旧 Python 服务，完整备份 `state.json + uploads/`，不要一边上传一边迁移。
2. 使用只读盘点脚本；退出码 2 时逐项核对缺失/孤立文件，不自动删除所谓“垃圾”。
3. 保留清单和原始目录，继续原工具或用备份恢复均不影响新 Go Server。

```bash
python3 scripts/legacy_inspect.py --data /absolute/path/shared_data > legacy-inventory.json
```

不要把输出文件重定向到旧 `state.json`。输出清单仍有文件 ID/路径等元信息，应作为本地项目资料保存。

## M3 导入器必须遵循的规则（尚未实现）

- 输入只读，导入到独立新数据库/文件目录；迁移事务记录来源 state SHA-256 和导入版本，允许幂等重跑。
- 保留原时间、原名称和原 clientId，但明确标注 `legacy-import`；clientId 不能授权或伪装新 Human/Bot 身份。
- 附件通过随机存储键接入、对比大小/哈希；缺失文件有明确错误，不写假索引。
- 非法编码提供逐项错误与用户决策，不静默丢弃整份历史。
- 消息计数、附件计数与 SHA-256、便签内容对账通过后才切换入口。
- 回滚仅恢复旧程序和完整备份，不允许两个版本共写目录。

## M1 自身数据

`metadata.json` 是 M1 的连接授权元数据，不是旧 Python 聊天状态。M3 SQLite 迁移还必须保留 Owner/Device 的信任关系与撤销状态，不得通过导入让已撤销设备复活。

## M2 元数据版本升级补充

M1 `metadata.json` 的 version=1 被 M2 打开时，先原样写入 `.v1.bak`，再升级为version=2并建立空services集合。设备凭证、Owner、证书仍沿用。M1程序拒绝version2；退回M1需停服后恢复升级前完整备份。不要让M1/M2同时写同目录。此项是Go内部元数据升级，不是Python聊天/文件导入。
