# CLI 与 API 契约入口

当前发布版本：**0.3.0-m3**。

M3 的实际命令、身份/消息/文件语义与权限边界见 [CLI-M3](CLI-M3.md)。原 M2 网络命令与数据通道语义继续适用，详见 [CLI-M2](CLI-M2.md) 和 [PROTOCOL-M2](PROTOCOL-M2.md)；新增的 `--allow-principals` 用于显式授权普通 Human/Bot，不代表设备权限继承。

机器可读合同：[OpenAPI 3.1](../api/openapi.json)。工程测试会检查合同版本、核心路由及本地 schema 引用，合同不是未经实现的未来路线图。

Agent 操作说明：[SKILL.md](../skills/seven-air-link/SKILL.md)，可由 `seven-air-link skill show` 离线输出。
