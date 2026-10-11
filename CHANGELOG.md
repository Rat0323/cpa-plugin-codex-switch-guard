# Changelog

All notable user-facing changes to Codex Switch Guard are documented here.
The project follows semantic versioning, and release automation publishes the
matching version section verbatim.

## [1.0.0] - 2026-10-12

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
  route relation, and the exact reasoning and compaction counts that a `strip`
  decision would remove, plus whether `previous_response_id` would be removed.
- Renamed the protection diagnostic action from `block_compaction` to
  `block_route_state` and added `reasoning_blocked`, `compaction_blocked`, and
  `previous_response_id_blocked` fields.

### Migration

- This is a clean rename release. The plugin ID, repository, release assets, and
  documentation use `codex-switch-guard`; the old repository URL redirects to the
  new repository.
- Remove the old `codex-switch-safe` library and configuration key before
  installing `codex-switch-guard`. CPA does not automatically remove the old
  local plugin file.
- No state migration is required. Earlier releases used the old plugin identity,
  so `1.0.0` creates a new state file on first start.
- Earlier Codex Switch Safe releases were removed from this repository as part of
  the clean rename. The source history remains available in Git.

[Unreleased]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/compare/v1.0.0...HEAD
[1.0.0]: https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/tag/v1.0.0
