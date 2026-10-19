# 在 Claude Code / Codex 中使用 Copilot Chat

Copilot Chat 内置本地转发服务。外部 Agent 只需要本地 Base URL 和固定的本地 key，上游账号的认证与续期由 Copilot Chat 负责。服务随主程序运行，无需独立 EXE、PowerShell、凭据 helper 或额外运行时。

## 配置

1. 在 **Settings → Providers** 登录 GitHub Copilot 或 Microsoft 365 Copilot，等待模型加载完成。
2. 打开 **Settings → Extensions → Agent setup**，开启 **Local forwarding**。默认监听 `127.0.0.1:39321`；端口占用时可以关闭转发、更改端口后重新开启。
3. 选择 Provider 和 Agent，复制生成的配置。每个 Provider 的地址形如 `http://127.0.0.1:39321/providers/<provider-id>`，不同账号不会互相回退。
4. Claude Code 合并到 `~/.claude/settings.json`；Codex 合并到 `~/.codex/config.toml`，然后重启 Agent。界面会生成实际地址和 key，无需手工填写占位符。

从旧版本升级时，删除原来的 `apiKeyHelper`、Provider `auth` 命令及旧的自定义 HTTP headers。Claude Code 还应清理冲突的 `ANTHROPIC_API_KEY`、环境变量及模型别名；Codex 删除旧的 `env_key`，避免多种认证配置同时生效。

Claude Code 的模型可以留空，通过 `/model` 选择。生成配置会开启 `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`；本地 `/v1/models` 将已加载、已启用且支持 Messages 的模型转换为 Claude Code 的目录格式。M365 原始服务的 `/models` 与这个接口不同，转换由转发层完成。

Codex 配置使用 `wire_api = "responses"`，并通过 `experimental_bearer_token` 保存固定本地 key。此处的 key 只用于本机服务，不是 Microsoft 或 GitHub 令牌。Codex 模型选择框只列出支持 Responses 的模型；不会将仅支持 Messages 的 Claude 模型伪装成 Responses 模型。

## 常驻与开机自启

**General → Keep running in the tray** 开启后，关闭主窗口会隐藏到托盘，转发继续工作。**Open at login** 使用操作系统的登录启动功能；当托盘常驻也开启时，登录启动会隐藏主窗口。macOS 可能需要在系统的 Login Items 中允许启动；页面显示系统实际状态及错误。

Agent setup 会分别显示托盘常驻、开机自启的“推荐开启”或“已开启”状态。转发开启时，退出程序会提醒连接中断并允许取消；关闭托盘常驻选项时也会提醒关闭主窗口将影响转发。提醒取消后保留原状态。关闭转发或确认退出会中断进行中的请求。

## 转发层的特殊处理

### 会话 ID

M365 推理要求 `X-ConversationId`。不能在复制的配置中写死一个所有聊天共用的值，也不能每一轮都换成新的 ID。

转发层依次读取以下标识，并用 Provider ID、标识来源及原值生成稳定的 UUIDv5：

| 来源 | 使用的标识 |
| --- | --- |
| Claude Code | `x-claude-code-session-id` |
| 当前 Codex | 优先 `thread-id`，其次 `session-id` |
| 较早的 Codex | `session_id` |
| 其他客户端 | 显式提供的 `X-ConversationId` |

同一 Provider、同一会话在多轮请求及 Copilot Chat 重启后保持一致；不同 Provider 或会话使用不同值。Codex 的 `session-id` 可能用于共享缓存亲和性，因此有真实 `thread-id` 时优先使用它。没有任何会话标识的客户端会被视为独立调用，每次生成新 ID；需要连续会话时应传入 `X-ConversationId`。

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

支持 `POST /v1/messages`、`POST /responses`、`POST /chat/completions`，以及 OpenAI 风格的 `/v1/responses`、`/v1/chat/completions` 别名。`/v1/messages/count_tokens` 转给上游；服务不支持时返回其错误，不捏造 token 数量。暂不提供 Responses WebSocket、远程压缩、文件上传、Batch 等其他 API。

请求 JSON 与响应正文原样传输，包括工具调用、图片、thinking 签名、`cache_control`、未知扩展字段和缓存用量。SSE 数据到达后立即刷新到 Agent，不等待整条回答结束。转发层不会重写系统提示词、拼接本地历史、注入工具或自动重试推理请求。`Anthropic-Version`、`Anthropic-Beta`、`OpenAI-Beta` 和 `beta=true` 会透传；其他客户端私有请求头不直接送往上游。

缓存是否命中仍取决于上游和 Agent 的提示词。稳定会话 ID 与保留缓存标记有助于维持请求语义，但不能保证每次命中。上游的 `cache_read_input_tokens`、`cached_tokens` 等字段会原样返回，不会被统一写成 0。

### 认证与生命周期

服务仅绑定 IPv4 `127.0.0.1`。支持 `Authorization: Bearer <local-key>` 或 `x-api-key: <local-key>`；拒绝浏览器 Origin 请求。第一次开启时生成随机 key，并以明文保存到本地设置，之后保持不变。该 key 可访问本应用内已启用且已连接的 Provider，应像其他本地凭据一样保管。

转发前替换为所选 Provider 的上游认证。上游令牌不返回给 Agent，不复制 Agent 的 Cookie，也不跟随上游重定向。没有任意 URL 转发入口。单次请求正文上限为 32 MiB。

隐藏的模型不会出现在目录中，新的推理请求也不能使用它。Provider 禁用、退出登录、删除或替换账号时，会撤销其正在进行的转发。Agent 断开连接会取消对应上游请求。模型仍由 Copilot Chat 现有的异步刷新机制管理。

## 参考

- [Claude Code 网关协议](https://code.claude.com/docs/en/llm-gateway-protocol)
- [Claude Code 模型配置](https://code.claude.com/docs/en/model-config)
- [Codex 配置参考](https://developers.openai.com/codex/config-reference/)
- [Codex 会话请求头源码](https://github.com/openai/codex/blob/main/codex-rs/codex-api/src/requests/headers.rs)
