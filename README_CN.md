# Codex Switch Safe

[English](README.md) | [简体中文](README_CN.md)

[![Build](https://github.com/Rat0323/cpa-plugin-codex-switch-safe/actions/workflows/build.yml/badge.svg)](https://github.com/Rat0323/cpa-plugin-codex-switch-safe/actions/workflows/build.yml)
[![Latest release](https://img.shields.io/github/v/release/Rat0323/cpa-plugin-codex-switch-safe)](https://github.com/Rat0323/cpa-plugin-codex-switch-safe/releases/latest)
[![License](https://img.shields.io/github/license/Rat0323/cpa-plugin-codex-switch-safe)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/Rat0323/cpa-plugin-codex-switch-safe)](go.mod)

一个用于保障 Codex 凭据和模型故障转移安全的原生 CLIProxyAPI（CPA）拦截器。它会在同一路由上保留加密上下文，并阻止与路由绑定的状态进入不同或未知的上游。

## 为什么需要它

Codex 请求可能包含由特定凭据和模型路由生成的加密 `reasoning` 与 `compaction` 状态。在使用多个账号的 CPA 环境中，轮询调度、额度耗尽、故障转移、配置重载或进程重启，都可能让已有会话被分配到不同的上游：

```text
会话首先使用凭据 A
-> CPA 随后选择凭据 B
-> 请求仍携带由 A 生成的加密状态
-> B 无法安全复用这些状态
-> 请求失败，或者会话必须从干净状态重新开始
```

Codex Switch Safe 会在请求到达所选上游之前补充这层路由感知保护。

## 如何保护请求

- 在相同 CPA 凭据和模型路由上原样保留有效的加密 reasoning。
- 在路由变化或无法确认时，移除不安全的顶层 `reasoning` 项和 `previous_response_id`。
- 默认阻止不安全的 `compaction`，也可以显式配置为移除后继续。
- 仅在 CPA 报告请求生命周期成功后提交路由变化；失败的重试和故障转移不会推进已确认路由。
- 默认使用有界的进程内状态；可选持久化可在正常 CPA 重启后保留已提交的
  opaque 路由事实，并通过 CPA 现有日志系统输出隐私安全的诊断信息。
- 忽略非 Codex 目标，不修改无关的请求内容。

插件不会解密任何加密内容，也不会修改用户提示、工具参数、嵌套代理消息或模型选择。

Codex Switch Safe 是路由安全边界，不是跨凭据上下文迁移系统。它无法解密由某个凭据生成的状态，也无法为另一个凭据重新加密。

## 选择 compaction 策略

| 目标 | 策略 | 结果 |
| --- | --- | --- |
| 不允许静默丢弃压缩上下文 | `block`（默认） | 对不安全的切换返回 HTTP 409 |
| 保持自动故障转移继续运行 | `strip` | 移除旧的路由绑定上下文，然后在新路由继续 |

只要已确认的凭据和模型路由没有变化，两种策略都会原样保留加密上下文。

## HTTP 409 是保护动作

如果 Codex 报告：

```text
Codex compaction belongs to a different or unknown upstream credential.
```

说明插件发现了无法确认属于当前所选路由的 compaction 状态。请求会在这些状态被发送到上游之前由本地拒绝。这表示保护机制已经生效，并不表示拦截器崩溃。

要继续任务，可以重新使用原始凭据、创建干净会话，或者设置 `compaction_policy: strip`，接受旧的压缩上下文被移除。

## 快速开始

要求：

- CLIProxyAPI `7.2.130` 或更高版本。
- 与 CPA 主机操作系统和架构匹配的发行文件。
- 已在 CPA 配置中启用插件。

可以从 CPA 插件市场安装，也可以从[最新发行版](https://github.com/Rat0323/cpa-plugin-codex-switch-safe/releases/latest)下载对应压缩包。每个压缩包根目录包含一个平台库文件：

| 平台 | 架构 | 库文件 |
| --- | --- | --- |
| Windows | `amd64`、`arm64` | `codex-switch-safe.dll` |
| Linux | `amd64`、`arm64` | `codex-switch-safe.so` |
| macOS | `amd64`、`arm64` | `codex-switch-safe.dylib` |

手动安装时，将库文件解压到 CPA 配置的 `plugins` 目录，然后重启 CPA。发行压缩包采用以下命名方式：

```text
codex-switch-safe_<version>_<goos>_<goarch>.zip
```

发行版还包含用于校验压缩包的 `checksums.txt`，以及记录源码提交、库文件大小和 SHA-256 的 `artifact-manifest.json`。

## 行为

| 请求和路由状态 | 默认行为 |
| --- | --- |
| 非 Codex 目标 | 不干预，直接通过 |
| 没有顶层路由绑定状态 | 原样通过 |
| 加密状态属于相同的已确认路由 | 原样通过 |
| 路由变化或未知 | 移除 reasoning 和 prior response ID |
| 不安全的 compaction（`block`） | 返回 HTTP 409 |
| 不安全的 compaction（`strip`） | 移除后继续 |
| 请求生命周期失败 | 不提交路由 |
| 请求生命周期成功 | 提交路由 |

插件只检查和移除 Responses 输入中的顶层项目，不会重写嵌套的 `agent_message` 内容或工具载荷。

## 配置

```yaml
plugins:
  enabled: true
  configs:
    codex-switch-safe:
      enabled: true
      compaction_policy: block
      persistence_enabled: false
      state_ttl: 4h
      max_sessions: 4096
      max_pending: 8192
      diagnostics: actions
```

| 字段 | 默认值 | 可选值 | 用途 |
| --- | --- | --- | --- |
| `enabled` | `true` | `true`、`false` | 插件实例开关 |
| `compaction_policy` | `block` | `block`、`strip` | compaction 处理策略 |
| `persistence_enabled` | `false` | `true`、`false` | 正常 CPA 重启后保留已提交的 opaque 路由状态 |
| `state_file` | 用户配置目录 | 绝对文件路径 | 可选持久化状态位置 |
| `state_ttl` | `4h` | `1m` 到 `24h` | 路由绑定保留时间 |
| `max_sessions` | `4096` | `1` 到 `65536` | 会话状态数量上限 |
| `max_pending` | `8192` | `1` 到 `65536` | 进行中请求数量上限 |
| `diagnostics` | `actions` | `off`、`actions`、`debug` | 插件诊断级别 |

`block` 是推荐的 compaction 策略，可以避免静默丢弃压缩上下文。只有在相比返回冲突，更希望请求丢弃旧上下文并继续时，才使用 `strip`。

两种策略仅在出现不安全的顶层 `compaction` 时存在差异。对普通加密 reasoning 和同路由续接的处理完全相同：

| 情况 | `block` | `strip` |
| --- | --- | --- |
| 相同的已确认凭据和模型路由 | 保留路由绑定状态并继续 | 保留路由绑定状态并继续 |
| 路由变化或未知，存在 reasoning 但没有 compaction | 移除不安全 reasoning 和 prior response ID，然后继续 | 移除不安全 reasoning 和 prior response ID，然后继续 |
| 路由变化或未知，并存在 compaction | 不向上游发送请求，返回 HTTP 409 | 移除 reasoning、compaction 和 prior response ID，然后继续 |
| 主要取舍 | 最大化上下文连续性安全；当前轮次可能需要回到原路由重试或干净启动 | 更高可用性；路由切换时可能丢失压缩上下文 |

当保留压缩会话上下文比完成当前轮次更重要时，适合使用 `block`。当自动凭据故障转移应该保持可用，即使新路由必须丢弃旧压缩状态时，适合使用 `strip`。

CPA `7.2.130` 暴露的插件配置字段没有独立的默认值属性。因此，在保存覆盖值之前，管理页面可能将这些字段显示为空。字段为空或省略都是有效配置，插件会应用上表中的运行时默认值。

插件注册时会拒绝未知配置字段。这样，字段拼写错误会产生明确的启动错误，而不会静默回退到其他策略。

CPA 插件优先级决定拦截器执行顺序。如果另一个请求拦截器会重写 Codex 请求体，应为 Codex Switch Safe 设置更低的优先级，使其在重写后执行最终安全检查。

### 可选持久化

设置 `persistence_enabled: true` 后，已提交的路由绑定、安全屏障、污染标记和失效信息可以在正常 CPA 重启后保留。持久化默认关闭，因此现有安装默认不会写入任何文件。

省略 `state_file` 时，状态会保存到用户配置目录：

```text
Windows: %APPDATA%\codex-switch-safe\route-state-v1.json
Linux/macOS: $XDG_CONFIG_HOME/codex-switch-safe/route-state-v1.json
            （或 ~/.config/codex-switch-safe/route-state-v1.json）
```

旁边的 `route-state-v1.json.key` 文件保存随机 32 字节主密钥，并通过域分离派生出指纹密钥和完整性密钥。状态文件只包含 opaque HMAC 指纹、时间戳、序列号和安全标志，绝不包含请求体、加密内容、原始会话 ID、auth ID、模型名或对话文本。

写入会先合并到临时文件，刷新后原子替换目标文件。进行中的请求状态不会持久化。如果密钥丢失，或状态文件损坏、被篡改、过大或无法读取，插件会从保守的 unknown-route 状态开始，并通过管理状态接口报告持久化错误。持久化失败不会削弱路由保护，也不会让不安全状态被发送到上游。

设置 `persistence_enabled: false` 会停止读取和写入状态文件，但不会删除已有文件。不再需要这些数据时，请手动删除状态文件和密钥文件。

### 推荐启用 CPA 会话亲和

在支持通用会话亲和的 CPA 版本中，建议启用它，以减少正常运行时的凭据切换：

```yaml
routing:
  strategy: round-robin
  session-affinity: true
  session-affinity-ttl: "4h"
  session-affinity-subagents: true
```

建议让 `routing.session-affinity-ttl` 与本插件的 `state_ttl` 保持一致。会话亲和负责主要的连续性保障；当故障转移、过期或手动路由仍然改变所选凭据时，Codex Switch Safe 作为最后一道安全保护。

## 诊断和隐私

| 级别 | 记录内容 |
| --- | --- |
| `off` | 不输出插件诊断信息 |
| `actions` | 保护动作及其最终生命周期结果 |
| `debug` | `actions` 的全部内容，以及安全通过决策 |

`actions` 是默认值，适合正常运行。只有在短期排查问题，需要确认插件是否观察、通过或清理了请求时才使用 `debug`。

诊断信息包括 CPA 请求 ID、动作、结果、移除数量，以及用于关联路由和会话的短进程内 HMAC 引用。诊断信息绝不包含：

- API key 或授权请求头。
- 原始 selected-auth、session、thread 或 conversation ID。
- 请求体、加密内容、reasoning 文本或用户对话文本。

插件没有网络访问权限。持久化默认关闭；开启后也只会写入上述 opaque 且带完整性校验的状态。默认进程内模式使用随机密钥生成路由和项目指纹，因此这些引用无法跨 CPA 重启关联。

### 管理状态接口

CPA 管理员可以查询经过管理鉴权的接口：

```text
GET /v0/management/plugins/codex-switch-safe/status
```

JSON 响应包含插件版本与运行时间、实际生效的安全配置、当前 session/pending/tainted/overflow 状态数量、累计保护动作和已跟踪请求完成计数，以及持久化是否启用和健康。持久化错误会以不含状态文件路径的形式返回。响应不会包含原始请求 ID、会话 ID、selected-auth ID、路由指纹、请求体或加密内容。该接口注册为 Management API 路由，不会通过 CPA 未经管理鉴权的插件资源路由暴露。

## 安全模型

- **稳定会话身份：** 优先使用 CPA `canonical_session_id`，然后依次使用 `execution_session_id`、Codex session/thread 请求头、载荷元数据和 `prompt_cache_key` 作为后备。忽略每轮独立的 ID。
- **路由身份：** 由 CPA `selected_auth_id`、所选模型和 `ToFormat: codex` 共同确定。
- **感知重试的生命周期：** 一个请求 ID 只保留最新的认证后候选项，并且只有结果为 `succeeded` 时才提交路由。
- **故障转移安全：** 失败、拒绝、取消和过期的尝试都不会推进已确认路由。
- **并发隔离：** 不同执行会话相互隔离。同一会话发生跨路由并发时会被标记为不可信，直到一次干净且成功的轮次建立新路由。
- **有界状态：** 可配置的 TTL 和条目上限同时约束内存状态和可选持久化状态。当已退休项目屏障溢出时，在会话状态过期前会保守地完整移除该会话的路由绑定状态。

插件不会串行化模型请求。面对并发结果不明确的情况，它会选择干净续接，而不是猜测哪个结果有效。

## 限制

如果服务端轮换密钥但继续使用相同的 CPA auth ID，插件在上游返回无效签名之前无法识别这种变化。收到该信号后，插件会使匹配的内存路由失效，并强制下一次续接从干净状态开始。

移除路由绑定状态可能减少当前轮次可用的 reasoning 上下文，但不会修改用户提示、工具、子代理消息或模型选择。持久化关闭时，CPA 每次重启都会重置路由状态。持久化开启时，已提交的 opaque 路由状态可以在正常重启后保留；进行中的请求和已过期的会话仍然不会保留。

## 开发

```powershell
$go = 'C:\Program Files\Go\bin\go.exe'
& $go test ./...
& $go test -race ./...
& $go vet ./...
```

原生构建需要 C 编译器，因为 CPA 通过原生 ABI 加载 Go 共享库。CI 会执行竞态、模糊测试、ABI 测试，以及针对 CPA `7.2.130` 和 `8.0.23` 的真实宿主兼容测试，并根据版本标签发布 Linux、macOS 和 Windows 发行文件。

## 贡献

开发检查、Pull Request 要求和发布流程请参阅 [CONTRIBUTING.md](CONTRIBUTING.md)。面向用户的变更记录在 [CHANGELOG.md](CHANGELOG.md) 中。

## 安全问题

安全敏感问题请通过 GitHub 私有漏洞报告提交，不要创建公开 Issue。支持版本和披露说明请参阅 [SECURITY.md](SECURITY.md)。

## 许可证

MIT，参阅 [LICENSE](LICENSE)。
