# 0.1.0-rc.3 — Password login and shared notepad

## Upgrade safely

Stop the rc.2 Server and back up its entire data directory. Using the new binary:

```bash
./seven-air-link storage backup --data ./server-data --out ./backup-before-rc3
./seven-air-link storage verify --from ./backup-before-rc3
./seven-air-link serve --data ./server-data --port 7777
```

The first open transactionally migrates SQLite schema 4 → 5. Identity, tokens,
messages, services, certificates, files and ACLs are preserved. The password
starts disabled and the shared note starts empty: no default password or
anonymous mode. This is not an import of the original Python `state.json` note;
paste retained legacy content manually. A pre-upgrade backup does not migrate
its source. Restore into a new directory using `storage restore --from BACKUP
--data NEW_DIR`, not `--out`. Old rc.2 rejects schema 5. Rollback requires the
pre-upgrade full backup, including certificates/tokens. Restoring a backup also
restores its authorization/password state; recheck credentials after recovery.
Backups contain sensitive data and are not encrypted.

## Fixed Owner Web password

Keep Server running; in another terminal:

```bash
./seven-air-link auth password set --config ./server-data/admin.json
# Equivalent for a nondefault server data directory:
./seven-air-link auth password set --data ./server-data
```

Input and confirm without terminal echo. Minimum 12 Unicode characters, maximum
1024 UTF-8 bytes; NUL/CR/LF rejected. No plaintext password command-line argument
or YAML field. Explicit `--password-stdin` supports protected automation input;
do not put passwords in shell history. Never share `admin.json` with Bots.

Windows opens `https://MAC_IP:7777`, selects **密码登录**, and enters the password.
It logs in as **Owner**, not a new per-device member. Give it only to trusted
administrators. Human/Bot members retain their separate identities and tickets.

Storage: a random 16-byte salt, PBKDF2-HMAC-SHA256 with 600,000 iterations and a
32-byte key in an independent SQLite table. This is not Argon2id or plain SHA256.
The unmodified official Go PBKDF2 implementation and license are vendored. TLS
1.3 and certificate pinning stay unchanged.

Change the password in **连接设置 → 网页登录密码**, providing the current password,
or recover a forgotten password using the Owner CLI. Changing/disabling it
revokes existing Owner Web cookies and pending tickets, including Owner proxy
login grants. Independent Node/Bot credentials and Owner CLI tokens remain.
Login attempts: 5/IP/minute, 30/global/minute, two concurrent KDFs. Actual socket
peer IP is used, not an untrusted forwarded header. No permanent account lockout.

```bash
./seven-air-link auth password status --config ./server-data/admin.json
./seven-air-link auth password disable --yes --config ./server-data/admin.json
./seven-air-link login create --config ./server-data/admin.json
./seven-air-link auth sessions list --config ./server-data/admin.json
./seven-air-link auth sessions revoke SESSION_ID --config ./server-data/admin.json
```

Disabling fixed passwords leaves ticket login; it never enables anonymous access.
Web cookies are Secure/HttpOnly/SameSite=Strict with CSRF protection, maximum
8 hours. Page reload preserves login; Server restart does not preserve sessions.
Each identity can list/revoke only its own Web sessions. `revoke all` includes
its current session. Proxy sessions are separate from management sessions.
Already completed external actions or downloaded data cannot be revoked back.

## Shared notepad

**共享便签** autosaves 800 ms after an edit pause, not during Chinese IME composition.
No send button. Text preserves whitespace/newlines, limited to 64 KiB UTF-8.
All active Human/Bot participants can read/write one shared board; Device
credentials cannot. This is not a private secret vault or document platform.

A separate SQLite row stores text/revision/author/time. Writes require
`baseRevision`; text, revision and metadata-only audit commit atomically. A stale
write returns 409 and the current note. The Web UI retains the local draft and
asks whether to adopt server text or explicitly save the draft at the latest
revision. Another intervening write causes another conflict, never a silent
force overwrite or automatic merge.

SSE sends the current snapshot on reconnect and latest committed revisions
(up to a 250 ms polling interval). Intermediate changes may coalesce; this is
not a historical revision log. Drafts remain only in this authenticated page,
not shared browser storage. Network errors preserve drafts; closing/reloading
warns about unsaved edits. Logout/identity change/authentication expiry clears
sensitive page state. Copy an important unsaved draft before ending a session.

## Human/Bot CLI

```bash
./seven-air-link note get --config ./bot.json --output json
./seven-air-link note set --revision 0 --file ./note.txt --config ./bot.json --output json
./seven-air-link note watch --timeout 30s --max-updates 2 --config ./bot.json --output ndjson
```

Use the revision returned by `get`, not a hardcoded value in repeated use.
`set` requires exactly one of `--text`, `--stdin`, `--file`, and `--revision`.
Conflicts exit nonzero with an `error` and the latest `note`. Reconcile; do not
blindly refresh-and-retry to overwrite somebody else's change. `watch` emits
latest snapshots, not exactly-once history. No A2A or model runtime is added.

API: `GET /api/v1/auth/methods`, `POST /api/v1/sessions/password`,
`GET|PUT|DELETE /api/v1/auth/password`, `GET /api/v1/sessions`,
`DELETE /api/v1/sessions/{id}`, `GET|PUT /api/v1/note`,
`GET /api/v1/note/events`. Note APIs follow existing identity and CSRF rules.
Password values/hashes and note text do not enter activity logs.

## Verification boundaries

Run `scripts/test.sh` for Linux regressions and `scripts/ui_rc3_test.py` for
real-browser HTTPS/SSE tests. Five native builds plus Linux real-browser tests
gate the immutable tag/release. Native CI is not a physical Mac↔Windows LAN,
firewall/sleep/wake or platform credential ACL validation. macOS programs remain
unsigned/not notarized; do not globally disable OS security.

The optional `seven-air-link open` convenience command is not implemented in
rc.3. Fixed password login removes the daily need to generate login tickets.
