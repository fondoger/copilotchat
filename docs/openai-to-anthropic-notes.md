本文档记录OpenAI与Anthropic API转换的特殊操作:

- 网页搜索：`web_search_20250305` → Responses `web_search`，指定搜索的 `tool_choice` 同步转换。上游执行搜索/打开页面，桥接只返回回答与去重的来源链接，不要求客户端执行工具，不伪造 Anthropic 加密搜索结果或原生搜索卡片；后续对话按普通文本回放。`max_uses` 转为顶层 `max_tool_calls`（限制全部内置工具调用，可能比原生搜索次数限制更严格）。目前不支持域名过滤、地域等搜索选项，传入会明确报错。
- 工具调用：`tool_use` / `tool_result` ↔ function call / output；保留调用 ID、JSON 参数和错误结果，转换工具选择与并行开关。
- 思考：thinking 配置/预算 → 模型支持的 reasoning effort；用桥接签名保留完整 reasoning 项，供无状态续聊回放。Desktop 超出的 effort 向下适配。
- 内容：system → 上游指令，轮次内 system/developer 保持顺序；图片/PDF 转 Responses 内容格式，文本附件转文本，JSON Schema 输出转 `text.format`。
- 响应：JSON/SSE 双向适配，转换结束原因和错误；用量拆分普通输入与缓存命中，避免重复计数。
- 路由：Desktop 模型别名还原上游 ID；转换后移除 Anthropic 专用版本/beta 请求头。token 计数走 `/responses/input_tokens` (Github Provider暂不支持, 404原样返回)，不生成回答。原生 Messages 路由不经过上述协议转换。
- 反向桥接（Codex → Anthropic）：仅在模型没有原生 Responses 时，将 `/responses` 转为 Messages；函数工具和自由文本工具（如 `apply_patch`）一并转换，后者用字符串参数包装，不保证 OpenAI CFG 约束。原生 Responses 优先；Messages-only 模型不支持 Responses compact/input_tokens 辅助端点，轮次中的新增 system/developer 指令也不能无损转换（仅支持开头指令）。
- 反向搜索与历史：`web_search` / `web_search_preview` → `web_search_20250305`，返回搜索事件与 URL 引用（引用范围按关联文本块近似）；通过桥接自有的 reasoning 回放封装保留真实 Anthropic 思考签名和搜索密文，不伪造原生密文。封装必须随完整历史回传；缓存式搜索、不对应的搜索选项、外来 reasoning/搜索历史会明确拒绝。混合工具允许搜索延迟到客户端工具结果返回后执行，跨响应结果按完整历史中的调用 ID 配对，不重复生成搜索调用。仅完成离线模拟验证，尚未实测 Anthropic 接口及真实 Codex 的封装保留行为。
- Tool 按需加载：生成的 Agent 配置默认开启客户端工具搜索，支持 Claude Code/Desktop 的 `tool_reference` 和 Codex 的 `tool_search_call` / `tool_search_output`；具体边界见下文。
- 显式缓存：支持双向断点转换，规则与限制见[显式缓存转换](details/explict-cache-convert.md)。

## 客户端工具按需加载

- **启用**：Claude Code 生成 `ENABLE_TOOL_SEARCH=true`，不再生成 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`；Desktop 生成 `toolSearchEnabled:true`；Codex 模型目录按工具能力声明 `supports_search_tool`。只生成配置，需用户 Apply/重启客户端；不启用 Codex Code Mode、Responses Lite 或已废弃的 tool-search feature flags。
- **关闭与阈值**：Claude Code 可手动设 `ENABLE_TOOL_SEARCH=false`，或以 `auto` / `auto:N` 使用比例阈值（默认 10%）；Desktop 可设 `toolSearchEnabled:false`。再次 Apply 会恢复生成值，用户 shell 中仍生效的 beta 总禁用需自行排查。
- **谁执行搜索**：由 Agent 搜索它自己的 MCP/Apps 工具目录，保留客户端的权限和执行循环。网关不执行工具，不维护跨请求工具注册表。工具加载仅影响 schema 进入模型上下文的时点，不意味着所有客户端的 HTTP 请求体都会变小。
- **Claude → Responses**：普通客户端搜索调用保持 function 调用；`tool_result.content` 中的 `tool_reference` 在原位置转为加载信息，从当前 `tools` 找对应 schema。未发现的 `defer_loading` 工具和占位项不发往上游，普通工具始终保留；未知引用明确拒绝。Claude Code 可以在发现后才把完整工具定义加入请求。原生 Messages 的 deferred/reference 内容与 headers 保持透传。
- **Codex → Messages/Responses**：`execution:client` 的搜索声明转成普通 function，历史搜索调用/结果同步转换；搜索结果中的工具定义供后续调用。返回恢复客户端搜索 item（object arguments）、调用 ID 和工具 namespace，支持 JSON/SSE。不同 namespace 的同名工具使用确定性别名，且与用户自行命名的 `tool_search` 函数区分。原生 Responses 接口也做此兼容，不能因协议名相同就假定上游支持 Codex 的搜索类型。
- **历史**：继续要求完整 input 历史，Messages 桥接的 reasoning 回放封装也要保留。移除搜索声明或 compact 请求没有 tools 时仍可转换遗留搜索历史，但不恢复整份历史工具表。只有原生 Responses 模型原本支持的 compact/input_tokens 端点可用；不会为 Messages-only 模型新增端点。工具搜索桥接不支持 `previous_response_id`、`conversation` 或 background 执行。
- **token 与缓存**：首轮只暴露搜索入口及非 deferred 工具；发现后将 schema 加入顶层工具表，可能改变该处缓存前缀。当前 GitHub/Microsoft 上游未确认支持原位 `additional_tools`，因此不发送该字段，不承诺无损缓存。搜索也需要额外的模型轮次，少量工具不一定更便宜。不在工具结果文本重复注入完整 schema；工具定义上的缓存断点仍明确拒绝。
- **不支持的语义**：本次不模拟 Anthropic hosted regex/BM25 或 OpenAI `execution:server` 搜索；不将其伪装成客户端搜索。解除 Claude Code beta 总禁用还可能带来其他 beta 请求，跨协议不能表达的 context-management、容器等语义明确拒绝；严格工具 schema 仅在目标支持时转换，不静默关闭 strict。用户/组织环境中仍生效的 beta 总禁用和客户端自身能力门控可能继续阻止搜索。
- **验证边界**：已覆盖离线多轮 HTTP mock、namespace、JSON/SSE、回放及原生透传；未使用真实 Anthropic 模型，未验证真实 Codex/Claude Desktop Chat/Cowork 各 surface 的工具搜索行为或测量 token 节省。Desktop 配置字段启用本身不等于全部客户端路径已实测。

参考：[Magpie issue 258](https://github.com/yetone/magpie/issues/258)、[Anthropic tool search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool)、[OpenAI tool search](https://developers.openai.com/api/docs/guides/tools-tool-search)。
