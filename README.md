# Seven-Air-Link

**Version: `0.1.0-rc.1` · lightweight cross-device development and AI collaboration bridge**

One native Go binary provides Server, Node and CLI modes; an embedded browser UI covers device pairing, service discovery, file sharing, messaging and audit. Human and Bot are independent identities with explicit permissions. Bot software can use the same CLI; no A2A or built-in Agent runtime.

**Default HTTPS/API port: `7777`.** Optional isolated HTTP reverse-proxy HTTPS port: `7778` (`--proxy-listen`). All cross-device connections use TLS. Do **not** expose the RC service to the public internet.

## Quick start: Mac Server → Windows Node

**Mac** (after downloading a native build from GitHub Actions / Releases):

```bash
./seven-air-link serve --data ./server-data --port 7777
# In a SECOND terminal (use the certificate fingerprint printed by serve):
./seven-air-link pairing create --config ./server-data/admin.json --ttl 5m
```

**Windows PowerShell**, in the unpacked directory with `seven-air-link.exe` and `libsqlite3-0.dll`:

```powershell
.\seven-air-link.exe join https://MAC_LAN_IP:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config .\windows-node.json
.\seven-air-link.exe node start --config .\windows-node.json
```

Then on Mac:

```bash
./seven-air-link devices list --config ./server-data/admin.json
./seven-air-link login create --config ./server-data/admin.json
```

Open `https://MAC_LAN_IP:7777` in a browser and enter the one-time login ticket. A certificate-trust prompt may appear; verify its fingerprint out of band. **Only browsers need no Node; exposing Windows localhost requires the Windows Node to stay running.** For actual cross-device forwarding, the Mac source side must also have a paired device identity: see [START-MAC-WINDOWS.md](START-MAC-WINDOWS.md).

## Native builds and releases

Run GitHub Actions **Seven-Air-Link · Native multi-platform builds** manually for build artifacts, or push the bare tag `0.1.0-rc.1` to publish a prerelease containing macOS ARM/Intel, Windows x64 and Linux x64/ARM64 ZIPs. See [BUILD-ON-GITHUB.md](BUILD-ON-GITHUB.md). macOS and Windows builds require their own cgo/SQLite-native toolchains; the Linux build here cannot substitute for target-platform validation.

## Existing M4 implementation and compatibility

This prerelease repackages the tested M4 functionality without replacing the database schema or re-authorizing existing identities. Earlier M3/M4 milestones are retained as [implementation history](docs/M4-IMPLEMENTATION-HISTORY.md), **not part of the version/tag**. Existing service/device credential files remain valid when given explicitly via `--data` and `--config`; new default configuration directories use the Seven-Air-Link name. Back up Server keys and database before any upgrade.

---

## Network service example

On **Windows** (after pairing and starting the Node), run an app on `127.0.0.1:8080` and share it only to your Mac device ID:

```powershell
.\seven-air-link.exe share http --name win-api --target 127.0.0.1:8080 --allow-devices MAC_DEVICE_ID --config .\windows-node.json
```

On **Mac** (with its own paired Node/identity configuration), run:

```bash
./seven-air-link services list --config ./mac-node.json --output json
./seven-air-link proxy open win-api --listen 127.0.0.1:18080 --config ./mac-node.json
# In another terminal:
curl http://127.0.0.1:18080/
```

The target application stays bound to Windows `127.0.0.1` and is never implicitly exposed to the LAN.

## Bot CLI integration

Use a distinct Bot identity rather than Owner credentials. Example:

```bash
./seven-air-link bot bind --name "Mac Coding Bot" --out ./mac-bot.json --config ./server-data/admin.json
./seven-air-link peers list --config ./mac-bot.json --output json
./seven-air-link message send --to BOT_ID --text "Check /health" --config ./mac-bot.json --output json
./seven-air-link skill show
```

A remote agent must actively read messages via `message watch`/`message list`; Bridge does not run models or treat message acceptance as task completion.

## Storage and upgrade compatibility

SQLite file `bridge.db`, Server certificates, authentication profiles, and authorized Node service definitions are all sensitive and should be backed up separately. If reusing an old Server data directory, keep `--data` explicit and **update the `server` URL in existing Owner/Node/Bot profile files** (or pass `--server https://HOST:7777`) when changing from port 8765 to 7777. Keep the pinned fingerprint and credentials unchanged; only the address/port changes. Do not try to downgrade a migrated SQLite directory into an old JSON-based binary.

Maintenance commands `storage backup|verify|restore|migrate|gc|check` require Server stopped, and are documented in [docs/STORAGE-M4.md](docs/STORAGE-M4.md).

## Release limitations

`0.1.0-rc.1` is a developer preview. Native GitHub macOS/Windows binaries have **not** been built or tested in this Linux container. Their publication requires successful GitHub Actions runs. Real Mac↔Windows networking, Windows ACLs, browser trust and security review remain separate release gates. No public internet exposure is supported by default.
