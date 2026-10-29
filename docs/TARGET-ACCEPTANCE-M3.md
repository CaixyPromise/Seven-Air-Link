# M3 真实机器与浏览器补验清单

本轮 Linux 进程测试不能替代本表。**当前均待真实目标环境执行，不能预先勾选为通过。**

| 检查 | 操作 | 预期 |
|---|---|---|
| Mac ↔ Windows | 替换 M2 程序、备份并原地升级 | 旧配对/服务/批准保留，未自动共享额外端口 |
| 两个 Bot 配置 | Owner 创建邀请，目标机器 identity bind | 指纹核对，一次性领取，长期 token 不出现在 URL/argv |
| 私信 | A CLI send，B CLI watch --incoming-only | Unicode/换行正常，身份显示正确，正常实时到达 |
| 断开重连 | 关闭 B watch，A 发消息，再启动 B | 历史补读，游标正常，已处理 ID 去重 |
| 跨端浏览器 | 不同浏览器 profile 分别登录 Human/Bot | 私信只对参与者可见；Member 无管理操作 |
| 编辑交互 | 不同会话写草稿、切换、回复 | 草稿不串会话；回复仅同会话 |
| 文件 | 粘贴截图、拖入多文件、手选文件 | 进度与失败提示正常，SHA-256 相同，无法读取第三方私有附件 |
| 编码 | UTF-8/GB18030/Big5/UTF-16 文本 | 手动选择编码，按纯文本显示 |
| 网络授权 | Node 上 share；Owner 显式 grant Bot；Bot proxy open | 不继承 Device 权限；获授权后可用，受限目标失败 |
| 生命周期 | 轮换/撤销 Bot，或撤销绑定设备 | 旧 Cookie/SSE/映射/数据流失效；新 token 不复活旧票据 |
| Server 关停 | 正常停止与强制终止再启动 | 消息/附件可恢复，旧网络字节不重放 |
| 系统行为 | 睡眠/唤醒、Wi-Fi 切换、防火墙策略 | 状态明确，恢复有界；不绕过组织网络管理 |
| Windows ACL | 检查 profile、Server目录和批准文件 | 其他 OS 账号不能读取长期凭证；不只依赖 Unix mode |
| 目标浏览器 | Chrome/Edge/Safari、移动端 | 实际 Cookie/CSRF、SSE、剪贴板兼容性、下载行为正常 |

自动浏览器补验（在正常允许访问本地服务的测试机上）：

```bash
python3 scripts/ui_m3_test.py --binary ./seven-air-link --chromium /path/to/chromium
python3 scripts/ui_test.py --binary ./seven-air-link --chromium /path/to/chromium
```

前者覆盖新协同；后者覆盖原设备管理。测试允许忽略**测试夹具自签证书**的浏览器告警，CLI 信任另有 pin 测试；这不是日常使用时忽略未知证书的建议。本轮容器返回 `ERR_BLOCKED_BY_ADMINISTRATOR`，没有关闭策略或改用绕过方法。

目标机记录：程序 SHA-256、OS/浏览器版本、日期、网络隔离情况、每项结果和具体失败日志。未签名程序是否允许运行应遵循本机策略，不把全面禁用安全防护当作安装步骤。
