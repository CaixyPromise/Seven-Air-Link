# Seven-Air-Link 0.1.0-rc.1

First release-candidate build of the Seven-Air-Link brand on top of the M4 Go+SQLite implementation.

- Renamed CLI/binary, module, Web brand, Skill and release artifacts from previous project identity.
- Management/API default port **7777**, isolated proxy example port **7778**.
- Human/Bot identity, file/message sharing, device pairing, TCP forwarding, HTTP proxy, audit, SQLite data, offline backup and GC retained.
- Five native GitHub Actions build targets with SHA-256 validation; tag exactly `0.1.0-rc.1`.
- This RC remains a developer preview. Real Mac↔Windows functional and browser E2E testing, code signing, and full security assessment remain release gates. Earlier credential files and SQLite storage are supported via explicit `--data`/`--config` paths.
