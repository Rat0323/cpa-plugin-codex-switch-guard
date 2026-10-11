# Codex Switch Guard

[English](README.md) | [简体中文](README_CN.md)

[![Build](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/actions/workflows/build.yml/badge.svg)](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/actions/workflows/build.yml)
[![Latest release](https://img.shields.io/github/v/release/Rat0323/cpa-plugin-codex-switch-guard)](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/latest)
[![License](https://img.shields.io/github/license/Rat0323/cpa-plugin-codex-switch-guard)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/Rat0323/cpa-plugin-codex-switch-guard)](go.mod)

A native CLIProxyAPI (CPA) interceptor that keeps Codex credential and model
switches safe. It preserves encrypted context on the same route and prevents
route-bound state from crossing into a different or unknown upstream.

## Why it exists

Codex requests can contain encrypted `reasoning` and `compaction` state bound to
the credential and model route that created it. In a multi-account CPA setup,
round-robin routing, quota exhaustion, failover, configuration reloads, or
restarts can select a different upstream for an existing conversation:

```text
Conversation uses credential A
-> CPA later selects credential B
-> the request still contains encrypted state created on A
-> B cannot safely reuse that state
-> the request fails or the conversation must restart cleanly
```

Codex Switch Guard adds the missing route-awareness before the request reaches
the selected upstream.

## How it protects requests

- Preserves valid encrypted reasoning on the same CPA credential/model route.
- Blocks unsafe top-level `reasoning`, `compaction`, and `previous_response_id`
  state by default before a request reaches a different or unknown route.
- Strips that unsafe state only when explicitly configured for availability.
- Commits route changes only after CPA reports a successful request lifecycle
  outcome; failed retries and failovers do not advance the route.
- Persists bounded opaque route bindings across normal CPA restarts and emits
  privacy-safe diagnostics through CPA's existing logging system.
- Ignores non-Codex targets and leaves unrelated request content untouched.

The plugin never decrypts encrypted content and does not alter user prompts,
tools, nested agent messages, or model selection.

Codex Switch Guard is a route-safety boundary, not a cross-credential context
migration system. It cannot decrypt state created by one credential or
re-encrypt it for another.

## Choose a route-state policy

| Goal | Policy | Result |
| --- | --- | --- |
| Never discard route-bound context silently | `block` (default) | Reject an unsafe switch with HTTP 409 |
| Keep automatic failover running | `strip` | Remove old route-bound context and continue on the new route |

Both policies preserve encrypted context unchanged when the committed
credential and model route remains the same.

`block` is not involved in an ordinary same-route continuation. It activates
when the route is changed, unknown, or carrying a known retired item, and the
request contains route-bound state, turning an otherwise silent context loss
into an explicit choice. `compaction_policy` keeps its historical configuration
key for compatibility, but the policy now applies to encrypted reasoning,
compaction, and prior response state together.

## HTTP 409 is a protection action

If Codex reports:

```text
Codex route-bound state belongs to a different or unknown upstream credential.
```

the plugin found route-bound state that it could not verify against the selected
route. The request was rejected locally before anything was removed or sent
upstream. This means the protection worked; it does not mean the interceptor
crashed.

The response body also contains a `details` object with the policy, route
relation, and exact number of reasoning and compaction items that a `strip`
decision would remove, plus whether `previous_response_id` would be removed.
To continue, retry on the original credential, start a clean turn, or set
`compaction_policy: strip` and accept that the listed state will be removed.

## Quick start

Requirements:

- CLIProxyAPI `7.2.130` or later.
- A release asset matching the CPA host operating system and architecture.
- A CPA configuration with plugins enabled.

Install Codex Switch Guard from CPA's plugin store, or download the appropriate
archive from the
[latest release](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/latest).
Each archive contains one root-level platform library:

| Platform | Architectures | Library |
| --- | --- | --- |
| Windows | `amd64`, `arm64` | `codex-switch-guard.dll` |
| Linux | `amd64`, `arm64` | `codex-switch-guard.so` |
| macOS | `amd64`, `arm64` | `codex-switch-guard.dylib` |

For a manual installation, extract the library into CPA's configured `plugins`
directory and restart CPA. Release archives use this naming convention:

```text
codex-switch-guard_<version>_<goos>_<goarch>.zip
```

The release also includes `checksums.txt` for archive verification and
`artifact-manifest.json` for the source commit, packaged-library size, and
packaged-library SHA-256.

## Behavior

| Request and route state | Default behavior |
| --- | --- |
| Non-Codex target | Pass through without intervention |
| No top-level route-bound state | Pass through unchanged |
| Encrypted state on the same committed route | Pass through unchanged |
| Changed or unknown route (`block`) | Return HTTP 409 |
| Changed or unknown route (`strip`) | Strip reasoning, compaction, and prior response ID, then continue |
| Retired item replayed on the same route (`block`) | Return HTTP 409 |
| Retired item replayed on the same route (`strip`) | Strip only that item and continue |
| Failed lifecycle attempt | Do not commit the route |
| Successful lifecycle attempt | Commit the route |

Only top-level Responses input items are inspected for removal. Nested
`agent_message` content and tool payloads are not rewritten.

## Configuration

```yaml
plugins:
  enabled: true
  configs:
    codex-switch-guard:
      enabled: true
      route_affinity: true
      persistence_enabled: true
      # state_file: C:/path/to/codex-switch-guard-state.json
      compaction_policy: block
      state_ttl: 4h
      max_sessions: 4096
      max_pending: 8192
      diagnostics: actions
```

| Field | Default | Accepted values | Purpose |
| --- | --- | --- | --- |
| `enabled` | `true` | `true`, `false` | Plugin instance toggle |
| `route_affinity` | `true` | `true`, `false` | Prefer the credential already committed for the Codex conversation during scheduling |
| `persistence_enabled` | `true` | `true`, `false` | Keep opaque route bindings across CPA restarts |
| `state_file` | `<UserConfigDir>/codex-switch-guard/route-state-v1.json` | File path | Optional persistent-state location |
| `compaction_policy` | `block` | `block`, `strip` | Handling for unsafe route-bound state |
| `state_ttl` | `4h` | `1m` through `24h` | Route binding lifetime |
| `max_sessions` | `4096` | `1` through `65536` | Session entry cap |
| `max_pending` | `8192` | `1` through `65536` | Pending attempt cap |
| `diagnostics` | `actions` | `off`, `actions`, `debug` | Plugin logging level |

`block` is the recommended policy. It avoids silently discarding any route-bound
context. Use `strip` only when continuing without that context is preferable to
returning a conflict.

`route_affinity` only intervenes when the plugin has already committed a Codex
route for the conversation. In that case the scheduler prefers the same
credential; with no known route, an invalid route, or a credential absent from
the current candidate list, the plugin leaves CPA's existing scheduling strategy
unchanged. This feature does not replace the global scheduler.
On CPA `7.3.20` or newer, the plugin can see available credentials across
priority tiers and retain an existing lower-priority route. Older hosts limit
the scheduler to their offered candidates. CPA filters unavailable or
cooling-down credentials before calling the plugin, so affinity yields to
normal failover when the original credential is unavailable.

The policies handle confirmed same-route continuations identically. They diverge
whenever unsafe route-bound state or a retired item is present:

| Situation | `block` | `strip` |
| --- | --- | --- |
| Same committed credential/model route | Preserve route-bound state and continue | Preserve route-bound state and continue |
| Retired item replayed on the same route | Return HTTP 409 without removing anything | Strip only that item, then continue |
| Changed/unknown route | Return HTTP 409 without sending the request upstream | Strip reasoning, compaction, and prior response ID, then continue |
| Main tradeoff | Maximum continuity safety; the turn may require retrying on the original route or starting clean | Higher availability; encrypted context may be discarded on a route switch |

`block` is appropriate when preserving encrypted conversation context matters
more than completing the current turn. An encrypted reasoning or compaction item
may be the only reusable representation of earlier conversation state, so
silently removing it can make
the continuation incomplete in ways the user cannot see from the current prompt.
`strip` is appropriate when automatic credential failover should keep working
even if the new route must continue without the old route's encrypted state.

CPA `7.2.130` exposes plugin-owned configuration fields without a separate
default-value property. Management screens may therefore display these fields as
blank until an override is saved. Blank or omitted fields are valid: the plugin
applies the runtime defaults shown above.

The CPA plugin priority controls interceptor order. If another request
interceptor rewrites Codex request bodies, give Codex Switch Guard a lower
priority so it runs afterward and performs the final safety check.

## Diagnostics and privacy

| Level | Records |
| --- | --- |
| `off` | No plugin diagnostics |
| `actions` | Protection actions and their final lifecycle outcomes |
| `debug` | Everything in `actions`, plus safe pass-through decisions |

`actions` is the default and is suitable for normal operation. Use `debug` for
short-term troubleshooting when you need to confirm whether the plugin observed
and passed or sanitized a request.

Diagnostics include the CPA request ID, action, outcome, removal counts, and
short HMAC references for route/session correlation. They never
include:

- API keys or authorization headers.
- Raw selected-auth, session, thread, or conversation IDs.
- Request bodies, encrypted content, reasoning text, or user conversation text.

The plugin has no network access. By default it writes one versioned state file
under the operating system's user configuration directory. The file contains a
random HMAC key, opaque fingerprints, timestamps, sequence numbers, and safety
flags only; it does not contain raw auth/session/thread IDs, request bodies,
encrypted content, or conversation text. The directory and file are created
with restrictive permissions where the operating system exposes POSIX modes,
and updates use a same-directory temporary file plus atomic replacement.

Set `persistence_enabled: false` to keep all state process-local. After a restart
that configuration deliberately returns to `unknown` route handling. A custom
`state_file` is useful for service accounts or controlled backup locations; its
parent directory should be private to the CPA service user.

## Safety model

- **Stable session identity:** CPA `execution_session_id` first, then Codex
  session/thread headers, payload metadata, and `prompt_cache_key` fallbacks.
  Per-turn IDs are ignored.
- **Route identity:** CPA `selected_auth_id`, selected model, and
  `ToFormat: codex`.
- **Retry-aware lifecycle:** a request ID keeps only its latest after-auth
  candidate, and a route is committed only after outcome `succeeded`.
- **Failover safety:** failed, rejected, canceled, and expired attempts do not
  advance the committed route.
- **Concurrency isolation:** independent execution sessions remain isolated.
  Same-session cross-route concurrency taints the session until a clean
  successful turn establishes a new route.
- **Restart continuity:** successful route commits, invalidations, taint flags,
  and retired-item barriers survive normal CPA restarts. In-flight requests do
  not; after restart they are treated conservatively.
- **Bounded state:** configurable TTL and entry caps constrain memory and the
  persisted snapshot. An overflowed retired-item barrier causes conservative
  full stripping for that session until its state expires.

The plugin does not serialize model requests. It deliberately prefers a clean
continuation over guessing which concurrent result is valid.

## Limitations

The plugin cannot distinguish a server-side key rotation that reuses the same
CPA auth ID until the upstream reports an invalid signature. On that signal it
invalidates the matching route and forces the next continuation to be clean.

Removing route-bound state can reduce available reasoning context for a turn,
but it does not change the user prompt, tools, subagent messages, or model
selection. If persistence is disabled, unavailable, or recovered from a damaged
file, the affected route is unknown and the configured `block` or `strip`
policy applies. Persistence improves restart continuity; it does not make
encrypted state portable between credentials.

## Migrating from Codex Switch Safe

Version `0.3.0` renames the plugin from Codex Switch Safe to Codex Switch
Guard. Remove the old `codex-switch-safe` library before installing
`codex-switch-guard`, rename the CPA configuration key from `codex-switch-safe`
to `codex-switch-guard`, and do not load both libraries at the same time. The
HTTP error type is now `codex_switch_guard_reset_required`.

No state migration is required. Earlier releases kept all state in memory, so
the first `0.3.0` start creates a new state file.

## Development

```powershell
$go = 'C:\Program Files\Go\bin\go.exe'
& $go test ./...
& $go test -race ./...
& $go vet ./...
```

Native builds require a C compiler because CPA loads the Go shared library
through the native ABI. CI tests the plugin and publishes Linux, macOS, and
Windows release assets from version tags.

## Contributing

See [CONTRIBUTING.md](CONTRIBUTING.md) for development checks, pull request
expectations, and the release process. User-facing changes are tracked in
[CHANGELOG.md](CHANGELOG.md).

## Security

Report security-sensitive findings through GitHub private vulnerability
reporting instead of a public issue. See [SECURITY.md](SECURITY.md) for supported
versions and disclosure guidance.

## License

MIT. See [LICENSE](LICENSE).
