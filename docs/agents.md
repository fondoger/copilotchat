# 在 Claude Code / Codex 中使用 Copilot Chat

Copilot Chat 内置本地转发服务。外部 Agent 使用 profile 的 Base URL 和明文 key `copilotchat`，上游账号的认证与续期由 Copilot Chat 负责。服务随主程序运行，无需独立 EXE、PowerShell、凭据 helper 或额外运行时。

## 配置

1. 在 **Settings → Providers** 登录 GitHub Copilot 或 Microsoft 365 Copilot，等待模型加载完成。
2. 打开 **Settings → Extensions → Agent setup**，开启 **Local forwarding**。默认监听 `127.0.0.1:39321`；端口占用时可以关闭转发、更改端口后重新开启。
3. 点击标签栏右侧的 **+** 新建 profile；右键标签可重命名、复制或删除。在配置下方的 **Models** 中组合不同 Provider 的模型，右侧 **Enabled** 控制是否开放，并可编辑各自的 alias。修改自动保存。
4. 选择 Agent，复制配置。Claude Code 合并到 `~/.claude/settings.json`；Codex 合并到 `~/.codex/config.toml`，然后重启 Agent。

### Profile 与模型别名

每份 profile 都有独立的模型勾选状态、alias、Agent 配置选项和地址。例如：

```text
http://127.0.0.1:39321/my-claude-code-profile
http://127.0.0.1:39321/my-codex-profile
```

名称使用 1–64 个英文字母、数字、连字符或下划线，并以字母或数字开头。重命名会改变 URL，需重新复制配置；旧地址立即停止服务。删除 profile 会撤销其进行中的请求，其他 profile 不受影响。复制 profile 后，两份配置可以独立修改。

默认勾选已加载的模型，alias 使用原模型 ID。自动别名只按 profile 中已勾选的模型分配；重名时依次使用 `gpt-5.5`、`gpt-5.5 (2)` 等名字。取消勾选第一个后，第二个自动变为 `gpt-5.5`。手动编辑过的 alias 不会重新编号；同时启用的模型不能重名，手动别名冲突时页面会提示先改名。不同 profile 可以重名。Provider 暂时离线、禁用或目录暂时缺失不会重排别名。新加载的模型会自动加入；已取消的勾选会保留。

模型 API 的 `id`、`name`、`display_name` 均为 alias；Codex 目录的 `slug` 也为 alias。推理时，转发层根据当前 profile 的 alias 找到唯一的 Provider 和真实模型 ID，不会在其他账号或 profile 中回退。

Provider 页面隐藏的模型仍不会开放给 Agent；profile 可以进一步缩小模型范围。模型的实际 API 能力也保持不变：改名不会把 Messages 模型转换为 Responses 模型。

从 0.5.0 升级后，需要重新复制配置：原来的 `/providers/<id>` 地址已改为 profile 地址，自动生成的长 key 改为 `copilotchat`。更早版本还需删除 `apiKeyHelper`、Provider `auth` 命令及自定义 HTTP headers。清理冲突的 `ANTHROPIC_API_KEY`、环境变量和 Codex 的旧 `env_key`。

Claude Code 可通过 `/model` 选择模型。生成配置会开启 `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`；`/<profile>/v1/models` 返回当前 profile 中支持 Messages 的模型。Claude Code 自身只自动发现 ID 中包含 `claude` 或 `anthropic` 的模型，因此建议在 Claude profile 使用这类 alias，例如 `claude-work`。改名后重启 Claude Code 刷新目录。

**Model defaults** 默认折叠，展开后可分别为 `ANTHROPIC_DEFAULT_FABLE_MODEL`、`ANTHROPIC_DEFAULT_OPUS_MODEL`、`ANTHROPIC_DEFAULT_SONNET_MODEL`、`ANTHROPIC_DEFAULT_HAIKU_MODEL` 和 `CLAUDE_CODE_SUBAGENT_MODEL` 选择 alias。选择 **Not set** 时不生成该环境变量。选择绑定到具体 Provider 的模型，alias 改名或自动重新编号后，生成配置会跟着更新；模型被取消勾选或不可用时会提示重新选择，避免误用同名模型。配置预览默认折叠，复制按钮始终显示。

Codex 配置使用 `wire_api = "responses"` 和 `experimental_bearer_token = "copilotchat"`。本机验证的 Codex 0.146.0 尚不支持 `model_catalog_url`，因此生成配置采用 `model_catalog_json`：Copilot Chat 将每份 profile 的目录写入自己的数据目录，在模型或 alias 改动时自动更新。复制配置即可，无需手工创建文件；改动后重启 Codex 加载新列表。只列出支持 Responses 的模型。

Codex 的 **Remote compaction** 开关默认关闭，此时 provider `name` 为 `Copilot Chat Proxy`；开启后固定为 `OpenAI`，让 Codex 启用远程压缩路径。转发层支持 `/responses/compact`，仍按当前 profile 的 alias 路由，并保持同一会话 ID。实际是否支持压缩取决于上游服务；上游返回的成功结果或错误均透传。

服务同时提供 `/<profile>/models`（通用目录）和 `/<profile>/codex/models`（Codex 原生目录 API）。支持 `model_catalog_url` 的客户端可以使用后者。原生目录要求指令模板，使用随应用分发的 Codex 官方通用默认模板，来源与许可见第三方声明。

## 常驻与开机自启

**General → Keep running in the tray** 开启后，关闭主窗口会隐藏到托盘，转发继续工作。**Open at login** 使用操作系统的登录启动功能；当托盘常驻也开启时，登录启动会隐藏主窗口。macOS 可能需要在系统的 Login Items 中允许启动；页面显示系统实际状态及错误。

Agent setup 会分别显示托盘常驻、开机自启的“推荐开启”或“已开启”状态，可展开 **Options** 直接调整。转发开启时，退出程序会提醒连接中断并允许取消；关闭托盘常驻选项时也会提醒关闭主窗口将影响转发。提醒取消后保留原状态。关闭转发或确认退出会中断进行中的请求。

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

请求仅将顶层 `model` 从 alias 映射为上游模型 ID；工具调用、图片、thinking 签名、`cache_control` 和未知扩展字段的内容保持不变。上游响应正文原样传输，包括真实模型 ID 和缓存用量。SSE 数据到达后立即刷新到 Agent，不等待整条回答结束。转发层不会重写推理请求的系统提示词、拼接本地历史、注入工具或自动重试推理请求。`Anthropic-Version`、`Anthropic-Beta`、`OpenAI-Beta` 和 `beta=true` 会透传；其他客户端私有请求头不直接送往上游。

缓存是否命中仍取决于上游和 Agent 的提示词。稳定会话 ID 与保留缓存标记有助于维持请求语义，但不能保证每次命中。上游的 `cache_read_input_tokens`、`cached_tokens` 等字段会原样返回，不会被统一写成 0。

### 认证与生命周期

服务仅绑定 IPv4 `127.0.0.1`。支持 `Authorization: Bearer copilotchat` 或 `x-api-key: copilotchat`；拒绝浏览器 Origin 请求。`copilotchat` 是易读的本机明文 key，不是 Microsoft 或 GitHub 令牌。它适用于全部 profile；profile 用于组织模型，不是不同用户之间的权限边界。

转发前替换为所选 Provider 的上游认证。上游令牌不返回给 Agent，不复制 Agent 的 Cookie，也不跟随上游重定向。没有任意 URL 转发入口。单次请求正文上限为 32 MiB。

隐藏的模型不会出现在目录中，新的推理请求也不能使用它。Provider 禁用、退出登录、删除或替换账号时，会撤销其正在进行的转发。Agent 断开连接会取消对应上游请求。模型仍由 Copilot Chat 现有的异步刷新机制管理。

## 参考

- [Claude Code 网关协议](https://code.claude.com/docs/en/llm-gateway-protocol)
- [Claude Code 模型配置](https://code.claude.com/docs/en/model-config)
- [Codex 配置参考](https://developers.openai.com/codex/config-reference/)
- [Codex 会话请求头源码](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/requests/headers.rs)
