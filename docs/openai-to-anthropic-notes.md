本文档记录OpenAI与Anthropic API转换的特殊操作。

文档书写采用渐进式披露规则，当前主文档仅提及重点名词和概括，细节都放在各自的子文档里面。

- 网页搜索：`web_search_20250305` → Responses `web_search`，指定搜索的 `tool_choice` 同步转换。上游执行搜索/打开页面，桥接只返回回答与去重的来源链接，不要求客户端执行工具，不伪造 Anthropic 加密搜索结果或原生搜索卡片；后续对话按普通文本回放。`max_uses` 转为顶层 `max_tool_calls`（限制全部内置工具调用，可能比原生搜索次数限制更严格）。目前不支持域名过滤、地域等搜索选项，传入会明确报错。
- 工具调用：`tool_use` / `tool_result` ↔ function call / output；保留调用 ID、JSON 参数和错误结果，转换工具选择与并行开关。
- 思考：thinking 配置/预算 → 模型支持的 reasoning effort；用桥接签名保留完整 reasoning 项，供无状态续聊回放。签名在有体积收益时采用 zlib + Base64（`copilotchat-responses:z1:`），继续支持旧的未压缩签名；解压最多 32 MiB，损坏或超限时拒绝请求。Desktop 超出的 effort 向下适配。
- 内容：system → 上游指令，轮次内 system/developer 保持顺序；图片/PDF 转 Responses 内容格式，文本附件转文本，JSON Schema 输出转 `text.format`。
- 响应：JSON/SSE 双向适配，转换结束原因和错误；用量拆分普通输入与缓存命中，避免重复计数。
- 路由：Desktop 模型别名还原上游 ID；转换后移除 Anthropic 专用版本/beta 请求头。token 计数走 `/responses/input_tokens` (Github Provider暂不支持, 404原样返回)，不生成回答。原生 Messages 路由不经过上述协议转换。
- 反向桥接（Codex → Anthropic）：仅在模型没有原生 Responses 时，将 `/responses` 转为 Messages；函数工具和自由文本工具（如 `apply_patch`）一并转换，后者用字符串参数包装，不保证 OpenAI CFG 约束。原生 Responses 优先；Messages-only 模型不支持 Responses compact/input_tokens 辅助端点，轮次中的新增 system/developer 指令也不能无损转换（仅支持开头指令）。
- 反向搜索与历史：`web_search` / `web_search_preview` → `web_search_20250305`，返回搜索事件与 URL 引用（引用范围按关联文本块近似）；通过桥接自有的 reasoning 回放封装保留真实 Anthropic 思考签名和搜索密文，不伪造原生密文。封装必须随完整历史回传；缓存式搜索、不对应的搜索选项、外来 reasoning/搜索历史会明确拒绝。混合工具允许搜索延迟到客户端工具结果返回后执行，跨响应结果按完整历史中的调用 ID 配对，不重复生成搜索调用。仅完成离线模拟验证，尚未实测 Anthropic 接口及真实 Codex 的封装保留行为。
- Tool 按需加载：GPT Responses 使用原生客户端／服务端搜索，机制、客户端行为及网关转换与限制见[工具搜索与按需加载](details/tool-search-on-demand-loading.md)；Copilot 流式 ID 变化的兼容处理、历史重放与缓存实测见[Tool Search 流式兼容与缓存前缀](details/tool-search-stream-compatibility.md)。
- 显式缓存：支持双向断点转换，规则与限制见[显式缓存转换](details/explict-cache-convert.md)。
