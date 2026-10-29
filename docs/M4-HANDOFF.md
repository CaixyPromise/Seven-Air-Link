# M3 之后：先补验与持久层，再扩展

基线为 0.3.0-m3。Human/Bot、消息、附件、CLI/Skill、Web协同和 M2 Principal 服务使用已具备开发验收闭环。不得据此扩展 Agent Runtime / A2A / 模型识别。

## 优先级

1. **真实环境验收与发布门禁**：Mac/Windows、真实浏览器、防火墙/睡眠、Windows ACL；受支持 Go 工具链重建与漏洞扫描；评审成熟 WebSocket 依赖替换/独立审计，保留现有协议与故障测试；签名/公证。
2. **完成原 M3 存储欠账**：SQLite WAL 单 Server 本地数据库；事务性消息+幂等索引+审计；按 seq 查询而非全量克隆；JSON v3→SQLite可校验迁移/备份/回滚；取得可信依赖并锁版本，不把同一个 SQLite 文件放网络盘多机写。
3. **历史和生命周期**：Python state.json 的只读盘点、导入 legacy 标记、文件大小/哈希核对；不伪造认证来源；上传 part/orphan 清理、可重试文件删除、配额统计、保留/导出策略。先解决容量与一致性，再做全文检索。
4. **体验渐进完善**：活动筛选 UI、历史搜索、图片预览、对离线/已撤销对象的历史导航、更明确的本机配置选择；按真实反馈决定系统服务安装，不静默后台化。

## 已验证后不能回退的边界

- 旧身份 generation 不能在新 token 领取后复活；轮换/撤销关闭已有租约和流，并让 CLI 退出。
- 同 key 同请求只一个 message，同 key 改内容冲突；消息与审计同次持久提交。
- Owner 无第三方私信 API；Member 无管理权限；Device 不能充当消息发送者；文件引用不能扩大 ACL。
- Bot 经 `--incoming-only` 避免消费自己的回复，游标仍推进；没有自动模型运行保证。
- Web 换身份/logout 清缓存并阻断延迟响应串号；远程内容只作文本，Skill 不能覆盖工具授权。
- M1/M2 故障、TLS pin、Node 本机批准、FIN/RESET、SSE/WebSocket Upgrade、源端监听生命周期测试保持通过。

## 不能冒充已完成

无 SQLite、旧聊天导入、全文搜索、永久审计、多工作空间、细粒度 Bot service.manage、E2E 内容加密、HA/P2P、系统级剪贴板监听、生产安全认证。当前单工作空间的所有有效 Principal 都可发送共享消息；进一步消息 ACL 只有真实需求出现才加。
