# 客户端工具按需加载

Agent 先加载搜索入口及常用工具，需要时再搜索并加载其他工具的 schema，减少首轮工具上下文。搜索和执行仍由 Agent 负责，网关只转换协议，保留客户端权限流程，不维护跨请求工具注册表。

## 启用与关闭

生成的 Agent 配置默认开启工具搜索，需 Apply 后重启客户端：

- **Claude Code**：`ENABLE_TOOL_SEARCH=true`，不再生成 `CLAUDE_CODE_DISABLE_EXPERIMENTAL_BETAS=1`。可手动设 `false` 关闭，或以 `auto` / `auto:N` 使用上下文比例阈值（默认 10%）。用户 shell/组织策略中的 beta 总禁用和客户端自身能力门控仍可能阻止搜索。
- **Claude Desktop**：`toolSearchEnabled:true`，可手动设 `false` 关闭。
- **Codex**：模型目录按工具能力声明 `supports_search_tool`；不启用 Code Mode、Responses Lite 或已废弃的 tool-search feature flags。

再次 Apply 会恢复生成值。解除 Claude Code beta 总禁用也可能放开其他 beta，并非只对工具搜索例外。

## token 与缓存

首轮仅暴露搜索入口及非 deferred 工具，发现后将 schema 加入顶层工具表，可能改变该处缓存前缀。当前 GitHub/Microsoft 上游未确认支持原位 `additional_tools`，因此不发送该字段，不承诺无损缓存；工具定义上的缓存断点仍明确拒绝。

搜索需要额外的模型轮次，少量工具不一定更便宜。按需加载只影响 schema 进入模型上下文的时点，不意味着所有客户端的 HTTP 请求体都会变小。工具结果文本不重复注入完整 schema。

## 协议转换

### Claude → Responses

普通客户端搜索调用保持 function 调用；`tool_result.content` 中的 `tool_reference` 在原位置转为加载信息，从当前 `tools` 找对应 schema。未发现的 `defer_loading` 工具和占位项不发往上游，普通工具始终保留；未知引用明确拒绝。

Claude Code 可以在发现后才把完整工具定义加入请求。原生 Messages 的 deferred/reference 内容与 headers 保持透传。

### Codex → Messages / Responses

`execution:client` 的搜索声明转成普通 function，历史搜索调用/结果同步转换，搜索结果中的工具定义供后续调用。返回恢复客户端搜索 item（object arguments）、调用 ID 和工具 namespace，支持 JSON/SSE。不同 namespace 的同名工具使用确定性别名，且与用户自行命名的 `tool_search` 函数区分。

原生 Responses 接口也做此兼容，不能因协议名相同就假定上游支持 Codex 的搜索类型。

### 历史与辅助端点

继续要求完整 input 历史，Messages 桥接的 reasoning 回放封装也要保留。移除搜索声明或 compact 请求没有 tools 时仍可转换遗留搜索历史，但不恢复整份历史工具表。

只有原生 Responses 模型原本支持的 compact/input_tokens 端点可用，不会为 Messages-only 模型新增端点。工具搜索桥接不支持 `previous_response_id`、`conversation` 或 background 执行。

## 限制与验证

- 不模拟 Anthropic hosted regex/BM25 或 OpenAI `execution:server` 搜索，不将其伪装成客户端搜索。
- 跨协议不能表达的 context-management、容器等语义明确拒绝；严格工具 schema 仅在目标支持时转换，不静默关闭 strict。
- 已覆盖离线多轮 HTTP mock、namespace、JSON/SSE、回放及原生透传；用户记录的 Desktop 场景对比为 47.8k → 24.6k，降幅约 48%，仅代表该场景。未使用真实 Anthropic 模型验证，真实 Codex 与 Desktop Chat/Cowork 各 surface 的完整工具搜索行为仍需分别验证，不能仅凭配置开关或单次对比推断全部路径生效。

参考：[Magpie issue 258](https://github.com/yetone/magpie/issues/258) · [Anthropic tool search](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool) · [OpenAI tool search](https://developers.openai.com/api/docs/guides/tools-tool-search)
