# Seven-Air-Link

**Version: `0.1.0-rc.4` · lightweight cross-device development and AI collaboration bridge**

One native Go binary provides Server, Node and CLI modes; an embedded browser UI covers device pairing, service discovery, file sharing, messaging and audit. Human and Bot are independent identities with explicit permissions. Bot software can use the same CLI; no A2A or built-in Agent runtime.

**Default HTTPS/API port: `7777`.** Optional isolated HTTP reverse-proxy HTTPS port: `7778` (`--proxy-listen`). All cross-device connections use TLS. Do **not** expose the RC service to the public internet.

## One Server, independent clients (rc.4)

Only one machine runs `serve`. Other machines open its HTTPS address or use
`client login` / `client bind`; they do not start another Server.

```bash
# On the Server machine only (Mac or Windows)
./seven-air-link serve --data ./server-data --port 7777
# In another terminal on that SAME machine:
./seven-air-link auth password set --config ./server-data/admin.json
./seven-air-link login create --config ./server-data/admin.json
```

Use the Ticket at `https://localhost:7777` for **local Owner administration**.
A remote browser enters its display name + joining password and receives a
**distinct Member identity**, never Owner. Even two browsers on the same IP
using the same password are independent. IP is diagnostic metadata, not identity.
Only a valid Server Owner credential **and** a loopback connection permit global
management; no anonymous localhost auto-admin and no trust in forwarded headers.

For Windows CLI, use **连接设置 → 绑定本机 CLI** in its browser, then:

```powershell
.\seven-air-link.exe client bind https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name "Windows CLI" --config .\windows-client.json
```

Enter the one-use binding code. The CLI gets a separate revocable credential
under the browser's identity. Alternatively, `client login` creates a new Member
using the joining password. Never copy `admin.json` to another client or Bot.
To share Windows localhost, use `client node --out windows-node.json` with the
Member config, then `node start` with that Node config. No second Server.

**Upgrade:** back up the old data directory. Schema 5→6 retains identities,
credentials, messages, services, files and notes, but deliberately disables the
old rc.3 Owner password. Set a new joining password locally. Already paired
Nodes and separate Bot credentials remain. See [RC4 usage](docs/RC4.md) and
[identity/security model](docs/IDENTITY-BOUNDARIES.md).

Shared notepad autosave, SSE and revision-based conflict protection remain;
updates now retain actor and credential/session provenance. Browser capability
flags drive menus/buttons; the Server independently enforces every permission.

## Native builds and releases

GitHub Actions builds five native targets on main; the explicit release request creates an immutable RC tag and prerelease only after every build passes. Pushing a bare RC tag also triggers release builds containing macOS ARM/Intel, Windows x64 and Linux x64/ARM64 ZIPs. See [BUILD-ON-GITHUB.md](BUILD-ON-GITHUB.md). macOS and Windows builds require their own cgo/SQLite-native toolchains; the Linux build here cannot substitute for target-platform validation.

## Existing M4 implementation and compatibility

This prerelease retains the previous identity/network functionality and adds a transactional SQLite schema 5→6 identity migration. Existing identities are not re-authorized. Earlier M3/M4 milestones are retained as [implementation history](docs/M4-IMPLEMENTATION-HISTORY.md), **not part of the version/tag**. Existing service/device credential files remain valid when given explicitly via `--data` and `--config`; new default configuration directories use the Seven-Air-Link name. Back up Server keys and database before any upgrade.

---

## Network and Bot operations

Use the step-by-step [Mac ↔ Windows guide](START-MAC-WINDOWS.md). A Human Member
can create only its own Bot. A Node accesses only locally approved services.
A Bot/client can use only explicitly granted services; knowing the Server URL,
sharing the joining password, or being linked to a device grants no extra ACL.
`proxy open` listens on the machine actually running that CLI command.

## Storage and upgrade compatibility

SQLite file `bridge.db`, Server certificates, authentication profiles, and authorized Node service definitions are all sensitive and should be backed up separately. If reusing an old Server data directory, keep `--data` explicit and **update the `server` URL in existing Owner/Node/Bot profile files** (or pass `--server https://HOST:7777`) when changing from port 8765 to 7777. Keep the pinned fingerprint and credentials unchanged; only the address/port changes. Do not try to downgrade a migrated SQLite directory into an old JSON-based binary.

Maintenance commands `storage backup|verify|restore|migrate|gc|check` require Server stopped, and are documented in [docs/STORAGE-M4.md](docs/STORAGE-M4.md).

## Release limitations

`0.1.0-rc.4` is a developer preview. Native GitHub macOS/Windows binaries have **not** been built or tested in this Linux container. Their publication requires successful GitHub Actions runs. Real Mac↔Windows networking, Windows ACLs, browser trust and security review remain separate release gates. No public internet exposure is supported by default.

## 本轮身份修复验收

见 [本地验收报告](docs/ACCEPTANCE-IDENTITY.md)。源码已本地实现与验证，
当前尚未通过此轮工具推送或发布；不能把旧 rc.3 的 CI 当作本版结果。
