# Codex Switch Guard

[English](README.md) | [简体中文](README_CN.md)

[![Build](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/actions/workflows/build.yml/badge.svg)](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/actions/workflows/build.yml)
[![Latest release](https://img.shields.io/github/v/release/Rat0323/cpa-plugin-codex-switch-guard)](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/latest)
[![License](https://img.shields.io/github/license/Rat0323/cpa-plugin-codex-switch-guard)](LICENSE)
[![Go version](https://img.shields.io/github/go-mod/go-version/Rat0323/cpa-plugin-codex-switch-guard)](go.mod)

## 仓库身份

- 插件 ID：`codex-switch-guard`
- 仓库名：`cpa-plugin-codex-switch-guard`
- 当前版本：`v1.0.0`
- 发行资产：`codex-switch-guard_<version>_<goos>_<goarch>.zip`
- 旧仓库 `cpa-plugin-codex-switch-safe` 的地址会重定向到本仓库。
- Codex Switch Safe 的旧更新器不会自动升级到本插件，因为插件 ID 和发行资产名都已更换。

一个让 Codex 凭据和模型切换保持安全的原生 CLIProxyAPI（CPA）拦截器。它会在同一路由上保留加密上下文，并阻止与路由绑定的状态进入不同或未知的上游。

## 为什么需要它

Codex 请求可能包含由特定凭据和模型路由生成的加密 `reasoning` 与 `compaction` 状态。在使用多个账号的 CPA 环境中，轮询调度、额度耗尽、故障转移、配置重载或进程重启，都可能让已有会话被分配到不同的上游：

```text
会话首先使用凭据 A
-> CPA 随后选择凭据 B
-> 请求仍携带由 A 生成的加密状态
-> B 无法安全复用这些状态
-> 请求失败，或者会话必须从干净状态重新开始
```

Codex Switch Guard 会在请求到达所选上游之前补充这层路由感知保护。

## 如何保护请求

- 在相同 CPA 凭据和模型路由上原样保留有效的加密 reasoning。
- 默认阻止不安全的顶层 `reasoning`、`compaction` 和 `previous_response_id` 进入不同或未知的上游。
- 只有显式配置为移除后继续时，才会剔除这些不安全状态。
- 仅在 CPA 报告请求生命周期成功后提交路由变化；失败的重试和故障转移不会推进已确认路由。
- 默认持久化有界的不透明路由状态，使正常重启 CPA 后仍能判断原路由，并通过 CPA 现有日志系统输出隐私安全的诊断信息。
- 忽略非 Codex 目标，不修改无关的请求内容。

插件不会解密任何加密内容，也不会修改用户提示、工具参数、嵌套代理消息或模型选择。

Codex Switch Guard 是路由安全边界，不是跨凭据上下文迁移系统。它无法解密由某个凭据生成的状态，也无法为另一个凭据重新加密。

## 选择路由状态策略

| 目标 | 策略 | 结果 |
| --- | --- | --- |
| 不允许静默丢弃路由绑定上下文 | `block`（默认） | 对不安全的切换返回 HTTP 409 |
| 保持自动故障转移继续运行 | `strip` | 移除旧的路由绑定上下文，然后在新路由继续 |

只要已确认的凭据和模型路由没有变化，两种策略都会原样保留加密上下文。

`block` 不参与普通的同路由续接。它会在“路由变化或无法确认”，或“同一路由上重放已知已退役项目”且请求携带路由绑定状态时介入，把原本静默发生的上下文丢失变成一次显式选择。`compaction_policy` 为兼容保留原有配置键名，但现在同时作用于 reasoning、compaction 和 previous_response_id。

## HTTP 409 是保护动作

如果 Codex 报告：

```text
Codex route-bound state belongs to a different or unknown upstream credential.
```

说明插件发现了无法确认属于当前所选路由的路由绑定状态。请求会在任何内容被移除或发送到上游之前由本地拒绝。这表示保护机制已经生效，并不表示拦截器崩溃。

响应体还会包含 `details` 对象，列出策略、路由关系，以及 `strip` 决策将要移除的 reasoning 和 compaction 数量，并说明 `previous_response_id` 是否会被移除。要继续任务，可以重新使用原始凭据、开始干净轮次，或者设置 `compaction_policy: strip`，接受列出的状态被移除。

## 快速开始

要求：

- CLIProxyAPI `7.2.130` 或更高版本。
- 与 CPA 主机操作系统和架构匹配的发行文件。
- 已在 CPA 配置中启用插件。

待插件市场注册表更新合并后，可以从 CPA 插件市场安装；当前也可以直接从
[最新发行版](https://github.com/Rat0323/cpa-plugin-codex-switch-guard/releases/latest)
下载对应压缩包。每个压缩包根目录包含一个平台库文件：

| 平台 | 架构 | 库文件 |
| --- | --- | --- |
| Windows | `amd64`、`arm64` | `codex-switch-guard.dll` |
| Linux | `amd64`、`arm64` | `codex-switch-guard.so` |
| macOS | `amd64`、`arm64` | `codex-switch-guard.dylib` |

手动安装时，将库文件解压到 CPA 配置的 `plugins` 目录，然后重启 CPA。发行压缩包采用以下命名方式：

```text
codex-switch-guard_<version>_<goos>_<goarch>.zip
```

发行版还包含用于校验压缩包的 `checksums.txt`，以及记录源码提交、库文件大小和 SHA-256 的 `artifact-manifest.json`。

## 行为

| 请求和路由状态 | 默认行为 |
| --- | --- |
| 非 Codex 目标 | 不干预，直接通过 |
| 没有顶层路由绑定状态 | 原样通过 |
| 加密状态属于相同的已确认路由 | 原样通过 |
| 路由变化或未知（`block`） | 返回 HTTP 409 |
| 路由变化或未知（`strip`） | 移除 reasoning、compaction 和 prior response ID 后继续 |
| 同一路由重放已退役项目（`block`） | 返回 HTTP 409 |
| 同一路由重放已退役项目（`strip`） | 仅移除该项目后继续 |
| 请求生命周期失败 | 不提交路由 |
| 请求生命周期成功 | 提交路由 |

插件只检查和移除 Responses 输入中的顶层项目，不会重写嵌套的 `agent_message` 内容或工具载荷。

## 配置

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

| 字段 | 默认值 | 可选值 | 用途 |
| --- | --- | --- | --- |
| `enabled` | `true` | `true`、`false` | 插件实例开关 |
| `route_affinity` | `true` | `true`、`false` | 调度时优先选回当前会话已提交的 Codex 凭据 |
| `persistence_enabled` | `true` | `true`、`false` | CPA 重启后保留不透明路由绑定 |
| `state_file` | `<UserConfigDir>/codex-switch-guard/route-state-v1.json` | 文件路径 | 可选的持久化状态文件位置 |
| `compaction_policy` | `block` | `block`、`strip` | 不安全路由绑定状态处理策略 |
| `state_ttl` | `4h` | `1m` 到 `24h` | 路由绑定保留时间 |
| `max_sessions` | `4096` | `1` 到 `65536` | 会话状态数量上限 |
| `max_pending` | `8192` | `1` 到 `65536` | 进行中请求数量上限 |
| `diagnostics` | `actions` | `off`、`actions`、`debug` | 插件诊断级别 |

`block` 是推荐策略，可以避免静默丢弃任何路由绑定上下文。只有在相比返回冲突，更希望请求丢弃旧上下文并继续时，才使用 `strip`。

`route_affinity` 只在插件已经为会话确认过一条 Codex 路由时介入。此时调度阶段会优先选回同一个凭据；没有已知路由、路由已失效或凭据不在当前候选列表中时，插件会让 CPA 继续使用原有调度策略。该功能不改变全局调度算法。

在 CPA `7.3.20` 或更新版本中，插件可以查看所有优先级层级内的可用凭据，因此能保留原本位于较低优先级的会话路由。旧版主机只提供其当前候选。CPA 会先过滤不可用或冷却中的凭据；原凭据不可用时，插件会交回 CPA 正常故障转移。

两种策略对已确认的同路由续接完全相同；一旦发现不安全的路由绑定状态或已退役项目，二者才会分叉：

| 情况 | `block` | `strip` |
| --- | --- | --- |
| 相同的已确认凭据和模型路由 | 保留路由绑定状态并继续 | 保留路由绑定状态并继续 |
| 同一路由重放已退役项目 | 返回 HTTP 409，不移除任何内容 | 仅移除已退役项目，然后继续 |
| 路由变化或未知 | 不向上游发送请求，返回 HTTP 409 | 移除 reasoning、compaction 和 prior response ID，然后继续 |
| 主要取舍 | 最大化上下文连续性安全；当前轮次可能需要回到原路由重试或干净启动 | 更高可用性；路由切换时可能丢失加密上下文 |

当保留加密会话上下文比完成当前轮次更重要时，适合使用 `block`。加密 reasoning 或 compaction 可能是较早对话状态唯一的可复用表示，静默移除后，用户仅从当前提示往往无法判断丢了什么；`block` 的价值就是把这次有损降级变成显式决定。当自动凭据故障转移必须继续，即使新路由需要丢弃旧加密状态时，适合使用 `strip`。

CPA `7.2.130` 暴露的插件配置字段没有独立的默认值属性。因此，在保存覆盖值之前，管理页面可能将这些字段显示为空。字段为空或省略都是有效配置，插件会应用上表中的运行时默认值。

CPA 插件优先级决定拦截器执行顺序。如果另一个请求拦截器会重写 Codex 请求体，应为 Codex Switch Guard 设置更低的优先级，使其在重写后执行最终安全检查。

## 诊断和隐私

| 级别 | 记录内容 |
| --- | --- |
| `off` | 不输出插件诊断信息 |
| `actions` | 保护动作及其最终生命周期结果 |
| `debug` | `actions` 的全部内容，以及安全通过决策 |

`actions` 是默认值，适合正常运行。只有在短期排查问题，需要确认插件是否观察、通过或清理了请求时才使用 `debug`。

诊断信息包括 CPA 请求 ID、动作、结果、移除数量，以及用于关联路由和会话的短 HMAC 引用。诊断信息绝不包含：

- API key 或授权请求头。
- 原始 selected-auth、session、thread 或 conversation ID。
- 请求体、加密内容、reasoning 文本或用户对话文本。

插件没有网络访问权限。默认情况下，它会在操作系统的用户配置目录下写入一个带版本号的状态文件。文件只包含随机 HMAC 密钥、不透明指纹、时间戳、序列号和安全标记，不包含原始 auth/session/thread ID、请求体、加密内容或对话文本。在支持 POSIX 权限的系统上，目录和文件会使用限制性权限；更新通过同目录临时文件和原子替换完成。

设置 `persistence_enabled: false` 后，全部状态只保留在进程内，CPA 重启后会按“路由未知”处理。服务账号或受控备份位置可以显式设置 `state_file`，其父目录应只允许 CPA 服务用户访问。

## 安全模型

- **稳定会话身份：** 优先使用 CPA `execution_session_id`，然后依次使用 Codex session/thread 请求头、载荷元数据和 `prompt_cache_key` 作为后备。忽略每轮独立的 ID。
- **路由身份：** 由 CPA `selected_auth_id`、所选模型和 `ToFormat: codex` 共同确定。
- **感知重试的生命周期：** 一个请求 ID 只保留最新的认证后候选项，并且只有结果为 `succeeded` 时才提交路由。
- **故障转移安全：** 失败、拒绝、取消和过期的尝试都不会推进已确认路由。
- **并发隔离：** 不同执行会话相互隔离。同一会话发生跨路由并发时会被标记为不可信，直到一次干净且成功的轮次建立新路由。
- **重启连续性：** 成功提交的路由、失效状态、并发污染标记和已退休项目屏障会跨正常 CPA 重启保留；进行中的请求不会恢复，重启后会保守处理。
- **有界状态：** 可配置的 TTL 和条目上限限制内存及持久化快照。当已退休项目屏障溢出时，在会话状态过期前会保守地完整移除该会话的路由绑定状态。

插件不会串行化模型请求。面对并发结果不明确的情况，它会选择干净续接，而不是猜测哪个结果有效。

## 限制

如果服务端轮换密钥但继续使用相同的 CPA auth ID，插件在上游返回无效签名之前无法识别这种变化。收到该信号后，插件会使匹配路由失效，并强制下一次续接从干净状态开始。

移除路由绑定状态可能减少当前轮次可用的 reasoning 上下文，但不会修改用户提示、工具、子代理消息或模型选择。若持久化被关闭、不可用或状态文件损坏后被恢复，受影响会话会按“路由未知”处理，并应用配置的 `block` 或 `strip` 策略。持久化解决的是重启后的路由识别，不会让加密状态跨凭据移植。

## 从 Codex Switch Safe 迁移

`1.0.0` 是正式切换新身份的版本。插件 ID、仓库名、发行资产和文档都统一为
`codex-switch-guard` / `cpa-plugin-codex-switch-guard`。旧仓库地址会重定向到新仓库，
但 CPA 不会自动删除本地旧插件文件，旧更新器也不会自动升级，因为插件 ID 和发行资产名
都已更换。安装 `codex-switch-guard` 前，应从 CPA 插件目录
删除旧的 `codex-switch-safe` 库，并把配置键从 `codex-switch-safe` 改为
`codex-switch-guard`，不要同时加载两个库。HTTP 错误类型同步改为
`codex_switch_guard_reset_required`。

不需要迁移状态。早期版本按旧插件身份保存状态，`1.0.0` 首次启动时会创建新的状态文件。

早期 Codex Switch Safe 发行版已在这次干净改名中从本仓库移除，源码历史仍保留在 Git 中。

## 开发

```powershell
$go = 'C:\Program Files\Go\bin\go.exe'
& $go test ./...
& $go test -race ./...
& $go vet ./...
```

原生构建需要 C 编译器，因为 CPA 通过原生 ABI 加载 Go 共享库。CI 会测试插件，并根据版本标签发布 Linux、macOS 和 Windows 发行文件。

## 贡献

开发检查、Pull Request 要求和发布流程请参阅 [CONTRIBUTING.md](CONTRIBUTING.md)。面向用户的变更记录在 [CHANGELOG.md](CHANGELOG.md) 中。

## 安全问题

安全敏感问题请通过 GitHub 私有漏洞报告提交，不要创建公开 Issue。支持版本和披露说明请参阅 [SECURITY.md](SECURITY.md)。

## 许可证

MIT，参阅 [LICENSE](LICENSE)。
