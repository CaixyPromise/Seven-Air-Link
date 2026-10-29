# M3 架构与实现决策

版本 0.3.0-m3；2026-10-28。本文描述实际实现，不将后续规划写成已交付能力。

## ADR-01：普通身份，不新增 Agent 层

Owner 是 human Principal；其创建的 Human/Bot 都是 member Principal，持有独立凭证。类型仅影响 UI、命名和管理，不改变消息协议。设备继续是独立 subject。没有 Task 资源、A2A/MCP 协议、Agent Adapter、模型发现、模型调用、运行时或隐式远程执行。

同一身份可以在多台终端使用自身配置；可选 Device 关联用于生命周期联动，而非硬件身份认证/防复制证明。关联设备离线不影响消息身份；设备撤销则使关联身份失效。

## ADR-02：Owner 批准 + 一次性领取 + 凭证代际

创建身份只签发 5 分钟单次 enrollment；可在另一台机器核对 TLS 指纹后领取，或由显式 Owner CLI 创建并领取到新私有文件。默认身份有效期 30 天，创建可设 1 分钟～365 天。只持久化高熵 token 与 enrollment 的 SHA-256；HTTP/日志/消息中不隐式公开长期 token。配置文件依然是明文 Bearer credential；POSIX 0600，Windows ACL 尚需验收，不声称使用系统 Keychain。

轮换使 token 进入 pending 状态，旧 token 即失效；新 token 保持 Principal ID。服务端 Subject 携带仅在内存中的 credential-generation stamp，浏览器会话、票据、网络租约和数据通道均绑定代际，避免新 token 激活后旧能力复活。主动失效与运行期复核共同防止竞态。Member 不能创建、轮换或撤销身份，不能创建设备配对或管理 Node。

## ADR-03：一个工作空间，公共频道 + 参与者私信

首版只有固定 `shared` 频道，所有有效身份可见；私信仅收发双方可经 API 读取。Member 可以发现同工作空间的有效身份并联系，不引入自定义频道、组织、复杂收件 ACL。Owner 可以审计操作元数据，但消息 API 不提供读取第三方私信的管理后门。

这不是对 Server 运维人员的保密保证。Server 是可信中继/存储节点，磁盘数据未加密；具备宿主机权限者能读取 JSON 和附件。没有端到端消息加密。

消息有独立单调 `seq`，与活动 `event.seq` 不同。以 `(senderId, clientMessageId)` 保证保留期内幂等；同 key 不同请求返回冲突。消息与 `message.accepted` 审计在同次持久提交。`accepted` 不推导 delivered/read/executed，`replyTo` 只允许同一可见会话，不是任务依赖图。`correlationId` 只是关联元数据。

## ADR-04：SSE 是通知 + 可重放历史，不是执行队列

Browser 和 CLI 通过同一消息 API/SSE 使用资源；250 ms 周期检查持久序列，最多每批 100 条。客户端用游标补读、重连；Server 过滤不可见正文，同时通过 checkpoint 推进序列。至少一次交付，不承诺 exactly-once 外部副作用。

CLI cursor 绑定 Server 证书指纹、身份、会话筛选、incoming-only 模式。先写 stdout 再提交 cursor，崩溃可能重放已输出记录；调用者必须去重。`--incoming-only` 在 CLI 跳过自身发送、仍推进游标，减少自我回复循环。超时且网络正常可返回空结果，未能保持连接返回 `inbox_unavailable`。长订阅收到撤销/轮换/到期会结束，不无限重试失效身份。

`Principal.listening` 是当前 inbox 订阅数。没有订阅不等于 Bot 不存在，有订阅不等于 AI 在执行任务。Bridge 不负责调起模型，宿主 Agent 自己选择轮询或前台订阅。

## ADR-05：M2 网络不复制为 Agent 网络

原 `allowPrincipals` 现在可写入有效 member ID。Bot 使用自己的配置执行 `services list` / `proxy open`，走原网络 API 和数据协议。绑定设备不继承 `allowDevices`；Owner 管理权也不等于服务使用权。

本期 Member 没有 `service.manage`；注册新的本机服务仍须本机 Node 与已配对设备流程。不要把 Node 私有配置交给 Bot 来替代授权。需要更细粒度管理委托时另起实际需求，不先加入新角色体系。

轮换/撤销清理浏览器、租约与流；过期/关联设备撤销由相同 active 检查发现。实测发现并修复 `principal_inactive` 未被旧 forward 重试逻辑当成永久拒绝的问题，避免存量流已断但本地监听仍重试。

## ADR-06：文件是有 ACL 的附件，不是远程文件系统

文件直接流式写临时文件，计算 SHA-256，文件同步后随机存储键发布，最后提交元数据。文件名只作展示，不进入存储路径。单条消息最多关联八个附件；私有附件不能通过引用扩大原受众。下载 attachment/octet-stream/nosniff，客户端完成哈希检查后发布到未存在的目标路径。

上传完成后收件人即可通过文件列表读取，发送消息只是建立引用；Web 明示此语义。删除只允许上传者，先 tombstone 再 unlink，失败返回 cleanupPending。文件系统与元数据不是跨资源事务：崩溃可能产生 orphan/part，尚无自动 GC。已删除文件仍占元数据数量上限。不得据此宣称具备完整生命周期治理。

## ADR-07：明确存储欠账，不假称 SQLite 已落地

设计基线计划 M3 引入 SQLite。本轮 Go 依赖下载不可用，已有工程是无依赖 JSON；为了保留五平台离线分发，没有将系统 libsqlite、Python 子进程或不完整代码拼成跨平台数据库。

**实际实现仍是 schema 3 原子 JSON 快照，每次变更整体克隆/序列化/fsync/替换。** 上限 10,000 条消息、64 MiB 正文、256 Member、4,096 附件记录；满额拒绝新数据，不静默删除历史。适合低到中等频率的开发协同验收，不是无限审计或高吞吐 Broker。SQLite、全文检索、索引、事务 outbox、配额清理、旧 Python 历史导入均未交付。

M2→M3 先保存 v2 备份，保留旧服务/设备/证书/凭证。持久消息序列与运行期在线状态分开；Server 重启不伪造身份订阅在线。恢复旧备份后客户端游标若超过新状态会明确报错，不静默从零重做外部动作。

## ADR-08：Web 是统一操作界面，不是可信内容执行器

沿用 M2 无框架、无 CDN 的内嵌页面，增加协同空间/参与者、独立 Member 登录。文本与名称只通过 textContent 渲染，附件按文本解码预览。整页拖放/主动粘贴不代表系统级剪贴板监听。身份切换与 logout 清除消息、草稿、附件缓存和未完成上传，并用页面认证 epoch 拦截延迟响应，避免旧内容写入新身份界面。

Bot 收到的消息/文件属于不可信输入。Skill 提醒宿主模型尊重自身审批边界，不因为远端文字自称系统指令而运行命令或扩大权限。服务端 ACL 不替代宿主 Agent 的工具安全策略。

## 发布门禁未关闭

受限自维护 WebSocket 传输仍继承 M2，未完成成熟库替换/独立安全审计。构建是 Go 1.23.2；当前环境不能完成在线漏洞扫描。真实 macOS/Windows、防火墙/睡眠/证书与文件 ACL、真实浏览器联网、发布签名/公证未验收。UI 离线双替身检查不是网络 E2E。不能把这些限制藏在“生产就绪”措辞后。
