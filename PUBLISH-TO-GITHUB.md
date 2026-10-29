# Seven-Air-Link · Publish 0.1.0-rc.1

This repository is prepared for the **CaixyPromise/Seven-Air-Link** GitHub repository. The repo must exist before its first push. The project name, CLI, Web brand and artifacts are **Seven-Air-Link / seven-air-link**; default HTTPS/API port **7777**, optional isolated proxy **7778**.

## Quickest path (GitHub CLI installed and authenticated)

After cloning the provided `.bundle` into `Seven-Air-Link`:

```bash
cd Seven-Air-Link
# First remove the clone's local bundle origin.
git remote remove origin
# Creates a private repository and pushes the current main branch.
gh repo create CaixyPromise/Seven-Air-Link --private --source=. --remote=origin --push
# This tag does not include a milestone suffix or leading v.
git push origin 0.1.0-rc.1
```

`gh auth login` may be required once on your own machine. GitHub Actions will build five native platform archives and create a GitHub **prerelease** when all builds pass.

## Alternative: GitHub website (no gh CLI)

1. Create a **private, empty** repository named `Seven-Air-Link` in account `CaixyPromise` at <https://github.com/new>. **Do not** generate a README, LICENSE or .gitignore on GitHub; they already exist locally or may be added later.
2. Clone the supplied Git bundle: `git clone Seven-Air-Link-0.1.0-rc.1.bundle Seven-Air-Link`.
3. From the cloned directory, run:

```bash
git remote set-url origin https://github.com/CaixyPromise/Seven-Air-Link.git
git push -u origin main
git push origin 0.1.0-rc.1
```

4. Visit **Actions** for the five build jobs. After all succeed, download Mac (`darwin-arm64` or `darwin-amd64`) and Windows (`windows-amd64`) from **Releases → 0.1.0-rc.1**. Do not treat an incomplete/failed run as a published binary.

## Expected release files

- `Seven-Air-Link-0.1.0-rc.1-darwin-arm64.zip`
- `Seven-Air-Link-0.1.0-rc.1-darwin-amd64.zip`
- `Seven-Air-Link-0.1.0-rc.1-windows-amd64.zip` (includes SQLite DLL(s))
- `Seven-Air-Link-0.1.0-rc.1-linux-amd64.zip`
- `Seven-Air-Link-0.1.0-rc.1-linux-arm64.zip`
- matching `.zip.sha256` archive checksums

## Server on Mac, Node on Windows

**Mac**: `./seven-air-link serve --data ./server-data --port 7777`; in another terminal, `./seven-air-link pairing create --config ./server-data/admin.json --ttl 5m`. Record Server certificate fingerprint and pairing code.

**Windows PowerShell**: `./seven-air-link.exe join https://MAC_IP:7777 --fingerprint SERVER_SHA256 --name Windows-Dev --config ./windows-node.json` (enter the code when prompted), followed by `./seven-air-link.exe node start --config ./windows-node.json`. Both machines must be able to reach `7777/TCP`; avoid public internet exposure.

**Mac verification**: `./seven-air-link devices list --config ./server-data/admin.json`. For Web UI use `./seven-air-link login create --config ./server-data/admin.json` then open `https://MAC_IP:7777` after verifying the certificate.

For sharing Windows localhost to the Mac, a separate Mac Node identity must be paired; see [`START-MAC-WINDOWS.md`](START-MAC-WINDOWS.md).

## Security / build limitations

This is an **RC developer preview**, not a signed or security-audited stable release. GitHub-hosted macOS and Windows builds require native Go/cgo/SQLite toolchains, so binaries are not supplied from the Linux development container. GitHub Runner builds and platform tests must actually complete before binaries are called available. The release workflow does not remotely install or start Bridge on personal devices.
