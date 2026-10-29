# Changelog

This project uses release candidate tags of the form `MAJOR.MINOR.PATCH-rc.N`.

## 0.1.0-rc.1 — 2026-10-29

Initial Seven-Air-Link developer preview, renamed from Seven LAN Bridge.

- HTTPS Server, device pairing and authenticated Node connections.
- Authenticated TCP forwarding and constrained HTTP reverse proxy.
- Human/Bot identity and messaging, files, events, CLI and Skill integration.
- SQLite-backed storage, migration, maintenance, backup and recovery tooling.
- Default management port set to **7777** (optional HTTP proxy entry: 7778).
- Native macOS, Windows, and Linux build workflow (actual platform build results depend on CI).

**Limitations:** RC version; native Mac/Windows E2E and browser network verification remain release gates. This does not guarantee production security readiness.
