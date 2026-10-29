# Security Policy

## Supported versions

`0.1.0-rc.1` is a **developer preview**, not a security-audited stable release. There is no promise of production security support yet.

## Reporting vulnerabilities

Please **do not open public issues for undisclosed vulnerabilities**. Use GitHub's private vulnerability reporting if enabled for this repository, or contact the maintainers privately through their published contact channels. Do not send secrets or working exploit chains to public issue threads.

## Deployment scope

- Default management HTTPS port: 7777. Expose only on trusted networks.
- Device pairing and pinned TLS fingerprints are required; do not bypass verification.
- Do not publish Bridge ports directly to the internet or use it as an unrestricted proxy.
- Treat Bot identity files, Server keys, database backups, and Node configs as sensitive.
- Sharing a local service and executing commands are different permissions; Bridge does not grant remote Shell access by default.
- Current native WebSocket implementation and Windows/macOS behavior require independent audit/real-device validation prior to a stable release.

Please include a clear affected version, reproduction steps, environment, and impact in private reports.
