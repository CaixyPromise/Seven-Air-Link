# M4 目标平台与发布门禁

## 已在本机执行的范围

Linux amd64：源码/竞态/静态检查；独立进程M1/M2/M3回归；实际M3版本数据迁移/备份/恢复/回滚；两个独立Linux网络栈与不同IP、相同localhost端口隔离，真实链路关闭/恢复、撤销；离线浏览器DOM及UI统计。

以ACCEPTANCE.md与reports最终结果为准，不能把此清单自身视为测试证据。

## Mac/Windows仍需真实补验

安装相应原生C/SQLite开发依赖，使用受支持Go工具链构建。本轮没有M4这两个平台的发布程序；优先解决原生编译和文件锁/权限差异，再验证Server/Node/CLI及代理。核对实际证书信任、Windows ACL、休眠唤醒、防火墙、Wi-Fi切换、原始复制/拖放行为。至少Windows localhost服务经Mac回环映射访问、双方独立Bot消息与文件、撤销与恢复。不能用Linux名称改成Mac/Windows代替。

## 浏览器补验

在策略允许的环境运行 scripts/ui_m3_test.py。需要Owner/Bot独立会话、私信实时SSE、文件上传/编码预览、权限UI、会话撤销；另核对M4存储只读统计与身份切换清理。网络被策略阻止时保留BLOCKED；不能改策略绕过，也不能用离线截图记为通过。

## 发布前

更新并核验Go/SQLite补丁版本，运行govulncheck/系统包漏洞扫描并保留完整结果；复审cgo边界与WebSocket实现，选定成熟传输库或完成独立协议审计；签名/公证；掉电/磁盘故障、长时资源趋势、备份密钥保护。当前门禁仍为BLOCKED，不发布公网服务。
