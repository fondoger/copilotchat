# 在 Claude Code / Claude Desktop / Codex 中使用 Copilot Chat

Copilot Chat 内置 LLM Gateway 本地转发服务。外部 Agent 使用 profile 的 Base URL 和明文 key `copilotchat`，上游账号的认证与续期由 Copilot Chat 负责。服务随主程序运行，无需独立 EXE、PowerShell、凭据 helper 或额外运行时。

## 配置

1. 在 **Settings → Providers** 登录 GitHub Copilot 或 Microsoft 365 Copilot，等待模型加载完成。
2. 打开 **Settings → LLM Gateway**，开启 **Local forwarding**。默认监听 `127.0.0.1:39321`；端口占用时可以关闭转发、更改端口后重新开启。
3. 点击标签栏右侧的 **+** 新建 profile；右键标签可重命名、复制或删除。在配置下方的 **Models** 中组合不同 Provider 的模型，右侧 **Enabled** 控制是否开放。修改自动保存。
4. 选择 Claude Code、Claude Desktop 或 Codex 标签，点击 **Apply** 一键写入本机配置，成功后按提示重启对应客户端。也可以使用 **Copy configuration** 手动配置。

### 一键应用

- **Claude Code**：更新用户目录下的 `.claude/settings.json`，整体替换顶层 `env`、`modelPicker`、`modelSettings`，不保留旧 `env` 中的其他变量；删除 `apiKeyHelper`。顶层 `model` 仅在与新 `modelPicker.options` 中某个 `model` 完全一致时保留。其他顶层设置保留，写入 `"lastModifiedBy": "copilot-chat"`。已有文件未带此标记时，先在同目录保存原始文件为 `settings.copilot-chat-backup-<datetime>.json`；后续应用已标记的文件不重复备份。成功后重启 Claude Code CLI。
- **Claude Desktop**：Windows 使用 `%LOCALAPPDATA%\Claude-3p\configLibrary\`，macOS 使用 `~/Library/Application Support/Claude-3p/configLibrary/`。通过 `_meta.json` 中名称为 `Copilot Chat` 的条目识别并复用 `<uuid>.json`，优先更新当前启用的对应文件；没有时创建并注册。配置本身不添加额外标记字段，重新 Apply 时会移除旧版本写入的 `lastModifiedBy`。保留其他配置文件及 `_meta.json` 的其他信息，并将 `appliedId` 指向当前文件。成功后重启 Claude Desktop。
- **Codex CLI / ChatGPT Desktop**：更新用户目录下的 `.codex/config.toml`，新增或替换 `model_providers.copilot_chat`，设置顶层 `model_provider`、页面所选的默认 `model` 和本地 `model_catalog_json`。其他 provider、profile、MCP 设置和无关注释保留。成功后重启 Codex CLI 或 ChatGPT Desktop。

配置不存在时自动创建所需目录和文件。已有 JSON / TOML 无法解析时会显示错误并保留原文件。写入先在同目录生成完整的临时文件，再替换目标文件。

### Profile 与模型 ID

每份 profile 都有独立的模型勾选状态、Agent 配置选项和地址。例如：

```text
http://127.0.0.1:39321/my-claude-code-profile
http://127.0.0.1:39321/my-codex-profile
```

名称使用 1–64 个英文字母、数字、连字符或下划线，并以字母或数字开头。重命名会改变 URL，需重新复制配置；旧地址立即停止服务。删除 profile 会撤销其进行中的请求，其他 profile 不受影响。复制 profile 后，两份配置可以独立修改。

默认勾选已加载的模型。公开 ID 自动使用 Provider 前缀，例如 `github-copilot/gpt-5.5`、`m365-copilot/gpt-5.5`。不再支持 alias 编辑，旧 alias 被忽略且不能用于路由；升级后需重新复制 Agent 配置。新加载的模型自动加入，已取消的勾选会保留。

同一类型配置多个 Provider 时，生成的 ID 加入用户名，例如 `github-copilot/qinyuan/gpt-5.5`。计数包含暂时禁用或离线的账号，避免临时状态改变其他账号的公开 ID。同名账号通过来自内部 Provider ID 的稳定后缀区分；缺少用户名时使用稳定的账号标识。

API 始终接受简写 `github-copilot/gpt-5.5` 和用户名形式 `github-copilot/qinyuan/gpt-5.5`。简写有歧义时，按设置中持久保存的 Provider 顺序选择第一个已启用、已登录且在当前 profile 中开放模型、支持请求协议的账号，不使用 map 遍历顺序或随机负载均衡。需要固定账号时使用配置中生成的完整 ID。账号离线或模型被隐藏后，简写可能选择另一个满足条件的账号。

从 0.5.0 升级后，需要重新复制配置：原来的 `/providers/<id>` 地址已改为 profile 地址，自动生成的长 key 改为 `copilotchat`。更早版本还需删除 `apiKeyHelper`、Provider `auth` 命令及自定义 HTTP headers。清理冲突的 `ANTHROPIC_API_KEY`、环境变量和 Codex 的旧 `env_key`。

模型目录的 `id` 和 Codex 目录的 `slug` 均为完整公开 ID。Anthropic 目录的 `display_name` 使用上游模型名称和 Provider 名称，便于辨认账号；通用目录的 `name`、`display_name` 保持公开 ID。Provider 页面隐藏的模型不会开放给 Agent，profile 可以进一步缩小范围。

### Claude Code 配置

生成的 `settings.json` 包含 `env`、`modelPicker`、`modelSettings`，复制时需合并这三个根字段。`modelPicker.options` 列出当前 profile 可用的全部 Messages 和 Responses 模型，`replaceBuiltInOptions: true` 使选择器显示网关提供的选项。Responses 模型通过下述协议转换供 Claude Code 使用，不再依赖名字过滤的自动发现开关。

上游模型 ID 包含 `claude` 时，增加 **`behavesAs`**，将上游 ID 中的 `.` 替换为 `-`，例如 `claude-sonnet-4.6` 对应 `claude-sonnet-4-6`。这是 Claude Code 官方字段拼写，`behaveAs` 无效。其他模型不添加此字段。

非 Claude 模型在 `modelPicker` 和已选择的默认模型环境变量中统一追加 `[1m]`，例如 `github-copilot/gpt-5.5[1m]`，以启用 Claude Code 的 1M 上下文档位。网关同时接受有无后缀的 ID，路由时移除后缀，上游始终接收原始模型 ID。`modelSettings` 使用不带后缀的模型 ID，适用于两种形式。

非 Claude 模型在 `modelSettings` 中以完整 Gateway ID 为键配置：

- `maxEffortLevel` 取目录支持的最高 Claude 可识别等级；未提供命名等级时使用 Claude 最低的 `low` 上限，转换时不会为无推理能力的模型添加 reasoning。
- `autoCompactWindow` 为 `min(实际 context window × 0.85, 400000)`，向下取整，为回复预留空间；目录缺少窗口时使用 `"auto"`，不猜测模型容量。

所有模型的 `modelSettings` 都包含 `#realContextWindow`，以 `#` 前缀标识这是一项参考信息，记录目录中的实际窗口大小，不参与 Claude Code 的窗口计算；`0` 表示上游未提供。Claude 模型保留内置的窗口与 effort 预设，仅增加这个参考字段。它仍是合法 JSON 字段，`#` 是字段名的一部分，并非 JSON 注释语法。

这些字段分别需要 Claude Code v2.1.257（`behavesAs`）、v2.1.267（模型级 `maxEffortLevel`）和 v2.1.288（模型级 `autoCompactWindow`）或更新版本。

**Model defaults** 默认折叠，可分别为 `ANTHROPIC_DEFAULT_FABLE_MODEL`、`ANTHROPIC_DEFAULT_OPUS_MODEL`、`ANTHROPIC_DEFAULT_SONNET_MODEL`、`ANTHROPIC_DEFAULT_HAIKU_MODEL`、`CLAUDE_CODE_SUBAGENT_MODEL` 选择模型。**Not set** 不生成该变量。选择绑定到具体 Provider 的上游模型，生成时解析为完整 ID；模型不可用时提示重新选择。配置预览默认折叠，复制按钮始终显示。

### Claude Desktop 配置

Claude Desktop 标签位于 Claude Code 与 Codex 之间。生成的 JSON 使用当前 profile 地址和固定的本机 key `copilotchat-claude-desktop`，不包含 Claude Code 专用的 `env`、`modelPicker` 或 `modelSettings`：

```json
{
  "inferenceGatewayBaseUrl": "http://127.0.0.1:39321/claude-code",
  "inferenceGatewayApiKey": "copilotchat-claude-desktop",
  "inferenceProvider": "gateway",
  "inferenceCredentialKind": "static",
  "inferenceModelPricingEnabled": true,
  "inferenceModelPricingMultiplier": 1,
  "disableEssentialTelemetry": true,
  "disableNonessentialTelemetry": true,
  "disableNonessentialServices": true,
  "skipWebFetchPreflight": false,
  "disableDeploymentModeChooser": true
}
```

服务器根据请求的 API key 判断是否使用 Desktop 命名，无需为此创建独立 profile。使用该 key 获取 `/v1/models` 时，上游 ID 不包含 `claude` 的模型增加 `claude-desktop/` 前缀，例如 `claude-desktop/github-copilot/grok-4.7`；Claude 模型保持原来的公开 ID。Desktop 目录不添加 `[1m]`，输入窗口和最大输出通过 Models API 的 `max_input_tokens`、`max_tokens` 提供。

推理请求支持带 `claude-desktop/` 前缀的 ID，包括带用户名的形式，网关移除前缀后按原有规则路由。使用普通 key 的 Claude Code 与 Codex 目录不受此命名方式影响。

### Anthropic 模型目录

`GET /<profile>/v1/models` 按 Anthropic Models API 返回 `data`、`first_id`、`last_id` 和 `has_more`。只列出可直接使用 Messages API 或可转换为 Responses 的模型。

- `limit` 默认为 20，范围为 1–1000。使用 `after_id` 继续向后获取，或 `before_id` 获取前一页；两者不能同时使用。
- 模型按上游发布时间从新到旧排列；日期相同时保留稳定的 Provider 顺序。未知发布时间返回 `1970-01-01T00:00:00Z`，目录排序不会改变推理时的账号选择顺序。
- `max_input_tokens` 为上游公布的实际输入窗口，`max_tokens` 为最大输出上限；未知时返回 `null`，不会扩充为 1M。
- `capabilities` 复用上游能力信息，并补充已加载的视觉、PDF、推理和 effort 等信息。转换模型不会宣称支持尚未实现的 Anthropic 服务端工具或上下文管理 API，所有模型的 Batch API 支持均为 `false`。
- 复用上游提供的 `lifecycle`、`deprecated_at`、`retires_at`。`line` 根据上游 ID 中的 `-haiku-`、`-sonnet-`、`-opus-`、`-fable-`、`-mythos-` 匹配，不区分大小写；其他模型返回 `null`。默认包含 `active` 和 `deprecated`，可通过重复的 `lifecycle` 参数筛选；只有显式指定 `retired` 才列出已退役模型。

空目录返回 `data: []`、`first_id: null`、`last_id: null`、`has_more: false`。无效的分页参数或不在当前目录中的游标返回 400。

### Codex 配置

Codex 配置使用 `wire_api = "responses"` 和 `experimental_bearer_token = "copilotchat"`。本机验证的 Codex 0.146.0 尚不支持 `model_catalog_url`，因此生成配置采用 `model_catalog_json`：Copilot Chat 将每份 profile 的目录写入自己的数据目录，在模型或 Provider 改动时自动更新。复制配置即可，无需手工创建文件；改动后重启 Codex 加载新列表。只列出支持 Responses 的模型。

Codex 的 **Remote compaction** 开关默认关闭，此时 provider `name` 为 `Copilot Chat Proxy`；开启后固定为 `OpenAI`，让 Codex 启用远程压缩路径。转发层支持 `/responses/compact`，按当前 profile 的模型 ID 路由，并保持同一会话 ID。实际是否支持压缩取决于上游服务；上游返回的成功结果或错误均透传。

服务同时提供 `/<profile>/models`（通用目录）和 `/<profile>/codex/models`（Codex 原生目录 API）。支持 `model_catalog_url` 的客户端可以使用后者。原生目录要求指令模板，使用随应用分发的 Codex 官方通用默认模板，来源与许可见第三方声明。

## 常驻与开机自启

**General → Keep running in the tray** 开启后，关闭主窗口会隐藏到托盘，转发继续工作。**Open at login** 使用操作系统的登录启动功能；当托盘常驻也开启时，登录启动会隐藏主窗口。macOS 可能需要在系统的 Login Items 中允许启动；页面显示系统实际状态及错误。

LLM Gateway 会分别显示托盘常驻、开机自启的“推荐开启”或“已开启”状态，可展开 **Options** 直接调整。转发开启时，退出程序会提醒连接中断并允许取消；关闭托盘常驻选项时也会提醒关闭主窗口将影响转发。提醒取消后保留原状态。关闭转发或确认退出会中断进行中的请求。

## 转发层的特殊处理

### 会话 ID

M365 推理要求 `X-ConversationId`。不能在复制的配置中写死一个所有聊天共用的值，也不能每一轮都换成新的 ID。

转发层依次读取以下标识，并用 profile 的内部 ID、Provider ID、标识来源及原值生成稳定的 UUIDv5：

| 来源 | 使用的标识 |
| --- | --- |
| Claude Code | `x-claude-code-session-id` |
| 当前 Codex | 优先 `thread-id`，其次 `session-id` |
| 较早的 Codex | `session_id` |
| 其他客户端 | 显式提供的 `X-ConversationId` |

同一 profile、Provider 和会话在多轮请求及 Copilot Chat 重启后保持一致；不同 profile、Provider 或会话使用不同值。profile 重命名不会改变内部 ID。Codex 的 `session-id` 可能用于共享缓存亲和性，因此有真实 `thread-id` 时优先使用它。没有任何会话标识的客户端会被视为独立调用，每次生成新 ID；需要连续会话时应传入 `X-ConversationId`。

Copilot Chat 自身的聊天直接使用已保存的聊天 ID，后续追问和重新打开聊天不会改变它。后台标题请求也关联对应聊天。GitHub Provider 继续使用自身的会话亲和性请求头。

### M365 请求头与 CLI 验证

2026-10-19 对本机 Copilot CLI 包 `1.0.99-2` 进行了隔离测试：将测试进程的 HTTPS 请求送入本机模拟服务，返回合成模型目录与正常的 Anthropic SSE 回复。两组会话各完成两轮，观察到组内 `X-ConversationId` 不变、组间不同；该值是 UUIDv5，且不同于 CLI 的原始 session ID。`X-ClientCorrelationId` 和 `X-ClientCallId` 每次调用都变化。没有保存认证令牌，没有修改系统证书信任，也没有把合成测试消息发往 M365。

转发与原生 M365 聊天共用以下服务字段：

| 字段 | 行为 |
| --- | --- |
| `X-ClientType` | `GitHub`，已在 CLI 请求中确认 |
| `X-ClientFeature` | `LlmApiProxy` |
| `X-ClientScenario` | `Code` |
| `X-Llm-Api-Version` | `2026-09-28`，来自此次 CLI 请求 |
| `X-ConversationId` | 对应会话的稳定 ID |
| `X-ClientCorrelationId` / `X-ClientCallId` | 每次上游调用分别生成新的 UUIDv4 |
| `X-Initiator` / `X-Interaction-Type` | 用户轮为 `user` / `conversation-user`；工具结果和后台请求标记为 Agent 发起 |

这些服务字段按实测的 GitHub Copilot 路径处理。Copilot Chat 保留自己的 `User-Agent: CopilotChatNative/<version>` 和客户端版本。CLI 此次报告 `X-ClientVersion: 1.0.98`，且使用 `Anthropic/JS 0.94.0` 与 `x-stainless-*` SDK 信息；本程序没有假装运行该 JS SDK。UUIDv5 的命名空间也是本程序自有的，未声称复现 CLI 未公开的哈希算法。服务内部如何归因、计费或鉴别客户端不能仅凭抓包证明完全相同。

### 协议与缓存

在 profile Base URL 下支持 `POST /v1/messages`、`POST /responses`、`POST /responses/compact`、`POST /chat/completions`，以及 OpenAI 风格的 `/v1/responses`、`/v1/responses/compact`、`/v1/chat/completions` 路径。`/v1/messages/count_tokens` 转给上游；服务不支持时返回其错误，不捏造 token 数量。暂不提供 Responses WebSocket、文件上传、Batch 等其他 API。

上游原生支持请求协议时，仅映射顶层 `model`，保留其余请求字段与响应正文。SSE 数据到达后立即刷新到 Agent。原生路由透传 `Anthropic-Version`、`Anthropic-Beta`、`OpenAI-Beta` 和 `beta=true`；其他客户端私有请求头不直接送往上游。

Claude 的 `POST /v1/messages` 选择仅支持 Responses 的模型时，自动改发 `/responses`：

- 顶层 system 转为 instructions，消息转为 Responses input。Claude Code 在消息列表中追加的 system（环境说明、提醒）和 developer 消息保留角色与原有位置，不移入顶层指令，避免后续轮次改写已有缓存前缀。仅携带 effort 的空 system 消息不作为正文发送，最近一条消息上的 effort 用于当前请求。图片、文档、客户端工具调用及结果保留顺序与调用 ID，JSON 大整数不经过浮点转换。
- `max_tokens` 转为 `max_output_tokens`，最低为 Responses 要求的 16。effort、thinking 与 JSON schema 输出设置转换为对应字段，推理等级只使用目录支持的值。推理模型不转发可能被拒绝的 temperature、top_p；stop_sequences 没有 Responses 对应项，不转发。
- JSON 与 SSE 回复转为 Anthropic 消息和事件，包括工具参数增量、thinking、停止原因和 usage。上游失败或中途断流会返回错误，不作为成功的空回答。客户端断开或 Provider 撤销会取消上游请求。
- 使用 `store: false` 并请求加密 reasoning。完整 reasoning item 包装进 thinking signature，后续工具轮次还原，避免丢失加密状态。
- `cache_control` 没有 Responses 对应字段，转换时移除，由上游执行自动前缀缓存。Responses 的 `input_tokens_details.cached_tokens` 转为 `cache_read_input_tokens`，并从 Anthropic `input_tokens` 中扣除，避免重复计数。

转换支持常用客户端工具，不模拟 Anthropic 服务端工具（如其托管 web search）；不支持的块或工具返回明确错误。转换模型的 `/v1/messages/count_tokens` 返回 501，因为 Responses 没有对应计数 API。直接 Responses 和原生 Messages 调用保持原有协议。

缓存是否命中取决于上游和 Agent 的提示词。稳定账号路由、会话 ID 和请求前缀有助于保持缓存亲和性，但不保证每次命中。原生响应的缓存字段原样返回，转换响应使用上游真实计数。

### 认证与生命周期

服务仅绑定 IPv4 `127.0.0.1`。支持通过 `Authorization: Bearer …` 或 `x-api-key` 提交本机 key：普通客户端使用 `copilotchat`，Claude Desktop 使用固定值 `copilotchat-claude-desktop`；拒绝浏览器 Origin 请求。这些 key 不是 Microsoft 或 GitHub 令牌，均适用于全部 profile；profile 用于组织模型，不是不同用户之间的权限边界。

转发前替换为所选 Provider 的上游认证。上游令牌不返回给 Agent，不复制 Agent 的 Cookie，也不跟随上游重定向。没有任意 URL 转发入口。单次请求正文上限为 32 MiB。

隐藏的模型不会出现在目录中，新的推理请求也不能使用它。Provider 禁用、退出登录、删除或替换账号时，会撤销其正在进行的转发。Agent 断开连接会取消对应上游请求。模型仍由 Copilot Chat 现有的异步刷新机制管理。

## 参考

- [Anthropic Models API](https://platform.claude.com/docs/en/api/http/models/list)
- [Claude Code 网关协议](https://code.claude.com/docs/en/llm-gateway-protocol)
- [Claude Code 模型配置](https://code.claude.com/docs/en/model-config)
- [Claude Code settings 字段](https://code.claude.com/docs/en/settings-reference#modelpicker)
- [Responses reasoning 与多轮状态](https://developers.openai.com/api/docs/guides/reasoning)
- [Codex 配置参考](https://developers.openai.com/codex/config-reference/)
- [Codex 会话请求头源码](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/requests/headers.rs)
