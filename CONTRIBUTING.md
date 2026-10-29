# Contributing to Seven-Air-Link

Thanks for your interest in improving the project.

## Getting started

- Check existing issues and open a focused issue for larger changes.
- Use a supported Go toolchain and platform SQLite headers/libraries (`CGO_ENABLED=1`).
- Build and run `go test ./...`, `go vet ./...`, and `gofmt` before submitting.
- Prefer small pull requests with tests, reproducible behavior, and clear compatibility notes.
- Include explicit documentation for CLI/API contract changes, especially security and authorization.

## Pull requests

1. Create a feature branch from `main`.
2. Add tests, describe changes, and explain any data format or compatibility impacts.
3. Open a PR using the repository template and wait for CI checks.
4. Do not commit credentials, runtime databases, build outputs, or user data.

## Architecture principles

Human and Bot share the same identity/CLI interface. Do not add Agent-specific execution or orchestration layers by default. Keep device authorization, ACL enforcement, and network target constraints intact. Document any security-sensitive changes.

## Versioning

Release candidates use `0.1.0-rc.N` tags; releases are marked prerelease until native-platform and security gates are complete.
