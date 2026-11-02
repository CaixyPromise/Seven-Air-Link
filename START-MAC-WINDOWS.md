# Mac / Windows 接入指南

请按 [0.1.0-rc.4 完整操作步骤](docs/RC4.md) 部署。

只有一台机器执行 `serve`，其他机器用浏览器或 `client login` / `client bind` 接入。
macOS ZIP 与 Windows ZIP 必须使用同一版本；Windows 保持 EXE 与附带 DLL 同目录。
Owner 仅通过 Server 本机凭证管理；局域网加入密码从不提供管理权限。

macOS 程序仍未签名/公证，不能声称此次修复消除了 Gatekeeper 提示。核对来源、校验和并按系统的单应用信任流程处理；不要全局关闭安全保护。
