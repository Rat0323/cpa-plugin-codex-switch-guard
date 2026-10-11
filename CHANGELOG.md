# Changelog

All notable user-facing changes to Codex Switch Guard are documented here.
The project follows semantic versioning, and release automation publishes the
matching version section verbatim.

## [0.5.0] - 2026-10-12

### Added

- Added `route_affinity` (default `true`). When a Codex conversation already has
  a committed route, the scheduler prefers that credential; otherwise the plugin
  leaves CPA's existing scheduling strategy unchanged.
- On CPA `7.3.20` or newer, affinity can preserve an available committed route
  across priority tiers while yielding to host failover when it is unavailable.

### Fixed

- Aligned the SDK dependency with schema 5 and 6 registration tests and
  prevented mixed-provider scheduling from treating non-Codex candidates as
  Codex routes.

### Changed

- **Breaking:** `compaction_policy: block` now rejects before any unsafe
  route-bound state is removed. This includes encrypted reasoning,
  `previous_response_id`, unsafe compaction, and retired items replayed on a
  confirmed route. Only `compaction_policy: strip` continues with cleanup.
- Added structured `details` to the HTTP 409 response. It reports the policy,
  route relation, and the exact reasoning/compaction counts that a `strip`
  decision would remove, plus whether `previous_response_id` would be removed.
- Renamed the protection diagnostic action from `block_compaction` to
  `block_route_state` and added `reasoning_blocked`, `compaction_blocked`, and
  `previous_response_id_blocked` fields.

## [0.3.0] - 2026-09-15

### Added

- Persisted successful route bindings, route invalidations, concurrency taint,
  and retired-item barriers across normal CPA restarts.
- Added `persistence_enabled` (default `true`) and optional `state_file`
  configuration. Persistent state contains only a random HMAC key, opaque
  fingerprints, timestamps, sequence numbers, and safety flags.
- Added recovery behavior for missing, expired, or damaged state. The plugin
  returns to conservative unknown-route handling instead of trusting stale data.

### Changed

- Renamed the project, plugin ID, configuration key, release artifacts, and
  diagnostics from Codex Switch Safe / `codex-switch-safe` to Codex Switch
  Guard / `codex-switch-guard`.
- Renamed the reset-required HTTP error type to
  `codex_switch_guard_reset_required`.
- Clarified that `block` makes loss of unsafe compaction context explicit,
  while `strip` favors failover availability by accepting that loss.

### Migration

- Remove the old `codex-switch-safe` library before installing the new
  `codex-switch-guard` library. Do not load both at once.
- Rename the CPA configuration key from `codex-switch-safe` to
  `codex-switch-guard`.
- Update log filters from `codex-switch-safe` to `codex-switch-guard` and
  update clients that match the reset-required error type.
- No state migration is required. Earlier releases kept all state in memory.

## [0.2.3] - 2026-09-05

### Fixed

- Restored plugin registration with CLIProxyAPI `7.2.151` schema 5 hosts and
  future compatible host schemas by negotiating the highest schema supported
  by both sides.

### Compatibility

- No configuration migration is required.
- CLIProxyAPI `7.2.130` or later remains supported.
- Users running CLIProxyAPI `7.2.151` or later should upgrade to this release.

## [0.2.2] - 2026-08-30

### Fixed

- Restored native plugin registration with CLIProxyAPI schema 4 hosts while
  preserving schema 3 compatibility.

### Compatibility

- No configuration migration is required.
- CLIProxyAPI `7.2.130` or later remains supported.

## [0.2.1] - 2026-08-21

### Maintenance release

This release has no runtime behavior or configuration changes and requires no
migration. It retains the `0.2.0` route-safety behavior and continues to require
CLIProxyAPI `7.2.130` or later. Users already running `0.2.0` successfully may
treat this as an optional maintenance upgrade.

### Changes

- CI now tests both pure-Go (`CGO_ENABLED=0`) and native (`CGO_ENABLED=1`)
  configurations, including race detection and vet checks. Production shared
  libraries continue to use CGO and the CPA host ABI.
- Added `artifact-manifest.json` with the source commit, archive SHA-256,
  packaged-library SHA-256, and library size for integrity verification.
- Hardened the release workflow with least-privilege permissions, pinned GitHub
  Actions, metadata checks, and validation of all six platform assets before
  publication.
- Expanded installation, configuration, diagnostics, security, and contribution
  documentation.
- Added regression coverage for `compaction_policy: strip`.

### Compatibility

- No breaking changes or configuration migration are required.
- CLIProxyAPI `7.2.130` or later remains required.
- Release assets support Windows, Linux, and macOS on `amd64` and `arm64`.

### Verification and installation

Download the archive matching the CPA host together with `checksums.txt` and
`artifact-manifest.json`. Calculate the archive hash with `sha256sum` on Linux,
`shasum -a 256` on macOS, or `Get-FileHash -Algorithm SHA256` on PowerShell,
then compare it with the matching entry in `checksums.txt`. After verification,
replace the existing plugin library and restart CPA.

See the [installation guide](https://github.com/Rat0323/cpa-plugin-codex-switch-guard#quick-start)
for platform library names and configuration details.

The checksum and manifest files verify artifact integrity and identify the
source commit; they are not signed provenance or reproducible-build attestations.

### Rollback

To roll back, install the matching `0.2.0` archive and restart CPA. Process-local
routing state is reset when CPA restarts.

Full diff: [v0.2.0...v0.2.1](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.2.0...v0.2.1)

## [0.2.0] - 2026-08-15

### Highlights

- Added privacy-safe diagnostics for route decisions, protection actions, and
  request lifecycle outcomes.
- Added route-scoped retired-item barriers so a rejected failover candidate
  does not retire valid state from the original route.
- Made route tracking retry-aware and lifecycle-aware. Failed, rejected,
  canceled, and expired attempts do not advance the committed route.
- Preserved encrypted reasoning on the same selected CPA credential/model route
  while stripping only unsafe top-level route-bound state during switching.
- Expanded regression coverage for encrypted reasoning, compaction, retries,
  failover, concurrency, and diagnostics.

### Behavior

- Same-route encrypted reasoning passes through unchanged.
- Unknown, changed, expired, or ambiguous routes are handled conservatively so
  foreign encrypted state is not sent to a new upstream.
- Unsafe compaction returns HTTP 409 by default. Setting
  `compaction_policy: strip` continues without unsafe compaction context.
- A successful lifecycle outcome is required before a route and its
  route-bound item fingerprints are committed.
- Diagnostics default to `actions`; `debug` adds safe pass-through decisions,
  and `off` disables plugin diagnostics.

### Upgrade notes

- Requires CLIProxyAPI `7.2.130` or newer.
- Existing plugin configuration remains compatible.
- Release assets cover Linux, macOS, and Windows on `amd64` and `arm64`.

## [0.1.1] - 2026-08-14

### Fixed

- Committed selected credential routes only after successful upstream requests.
- Prevented failed failover candidates from retiring valid reasoning items.
- Added process-local keyed fingerprints for top-level route-bound items.
- Improved stable session identity detection and bounded-state failure handling.
- Completed live dual-provider switching validation and six-platform builds.

### Compatibility

- Requires CLIProxyAPI `7.2.130` or newer.
- Users of `v0.1.0` should upgrade to this release.

## [0.1.0] - 2026-08-14

Initial release. This version is deprecated because retry and session edge cases
were corrected in `v0.1.1`.

[Unreleased]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.4.0...v0.5.0
[0.3.0]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.2.3...v0.3.0
[0.2.3]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.2.2...v0.2.3
[0.2.2]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/tag/v0.2.0
[0.1.1]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/tag/v0.1.1
[0.1.0]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/tag/v0.1.0
