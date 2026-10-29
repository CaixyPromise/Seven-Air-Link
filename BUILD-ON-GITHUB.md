# Seven-Air-Link · GitHub Actions / RC release

Current version and **exact Git tag**: `0.1.0-rc.1` (no `v` prefix, no `m0`/`m1`/`m4`).

## Builds

- Push to `main` or use **Actions → Seven-Air-Link · Native multi-platform builds → Run workflow** to generate 5 downloadable workflow artifacts.
- `git tag 0.1.0-rc.1 && git push origin 0.1.0-rc.1` triggers native builds and automatic **prerelease** once all 5 jobs pass.
- Archives: `Seven-Air-Link-0.1.0-rc.1-darwin-arm64.zip`, `...darwin-amd64.zip`, `...windows-amd64.zip`, `...linux-amd64.zip`, `...linux-arm64.zip`.
- Release names and executable names contain only `Seven-Air-Link` and the semver RC tag.

Runner platforms: `macos-15` (Apple Silicon), `macos-15-intel`, `windows-2025`, `ubuntu-24.04`, `ubuntu-24.04-arm`. Each runner compiles natively against its own SQLite development library; Windows archive includes `libsqlite3-0.dll`.

The workflow checks the binary's embedded version/OS/arch, emits per-file and archive SHA-256, runs Go tests (native), and refuses mismatched tags. It does not install apps onto end-user computers. Release is marked prerelease rather than stable; platform signing/notarization and true Mac↔Windows integration tests remain outstanding.

## Publishing from a newly created GitHub repo

1. Create the repository `Seven-Air-Link` under your own account and push the repository contents, including `.github/workflows/`.
2. Check **Settings → Actions → General → Workflow permissions**; the release job needs write access to Contents via `permissions: contents: write` (workflows respect organizational policy).
3. Push `main`, verify CI artifacts; then push the exact tag `0.1.0-rc.1`.
4. Visit **Actions** for build logs and **Releases** for platform ZIPs. If a job fails, no prerelease is created. Do not turn off checks to force publication.

Do not commit `server.key`, `admin.json`, `bridge.db`, `.slb-server`, Bot/device config files or access tokens. These are operational secrets and are ignored by `.gitignore` in standard locations; check unusual paths before committing.
