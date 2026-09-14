# Contributing

Thanks for helping improve Codex Context Guard.

## Before opening a change

- Use an issue for reproducible bugs or behavior proposals.
- Use private vulnerability reporting for security-sensitive findings.
- Keep changes focused on Codex route-bound state safety and CPA plugin
  integration.
- Never include credentials, raw request bodies, encrypted reasoning, or user
  conversation content in tests, issues, commits, or logs.

## Development checks

```powershell
$go = 'C:\Program Files\Go\bin\go.exe'
& $go test ./...
& $go test -race ./...
& $go vet ./...
```

Native shared-library builds require a C compiler. Pull requests always run
tests and the aggregate `ci` check in GitHub Actions. Pull requests that change
only Markdown files or files under `docs/` skip the six native platform builds;
code and configuration changes run the full build matrix. Version tags and
manual workflow runs always build all supported platforms.

The local `dist/` and `smoke/plugins/` directories are disposable build output.
They are intentionally ignored and are not release evidence. GitHub Release
assets are authoritative. Each release includes `checksums.txt` for archives
and `artifact-manifest.json` for the SHA-256 and size of the library inside
each archive, along with its source commit.

## Pull requests

- Explain the behavior change and its safety implications.
- Add focused regression tests for state, retry, failover, or sanitization
  changes.
- Update README configuration or diagnostics documentation when behavior changes.
- Add user-facing changes under `[Unreleased]` in `CHANGELOG.md`.
- When a change will be released immediately, its final pull request may also
  prepare the release metadata. Use a dedicated release-preparation pull request
  when batching multiple changes or preparing a release separately.

## Releases

Before release, move the relevant changelog entries from `[Unreleased]` into a
dated version section and synchronize the version in `main.go`, `Makefile`, and
`marketplace/registry-entry.json`. These updates may be included in the final
change pull request for an immediate release or in a dedicated
release-preparation pull request.

After the pull request containing the release metadata is merged and its checks
pass, create and push the matching `v<version>` tag from the verified `main`
commit. The release workflow validates metadata and checksums, creates a draft
release, uploads all platform assets, verifies the asset list, and then
publishes it.
