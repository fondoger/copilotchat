# 显式缓存转换

OpenAI 新版 Responses（官方文档为 GPT-5.6 及之后的模型）支持 `prompt_cache_options` 和内容块上的 `prompt_cache_breakpoint`；Anthropic 使用 `cache_control`。较早的 OpenAI 模型只有隐式缓存。本网关仅对 Auto 模式安全 classifier 的 Messages → Responses 请求转换显式断点，包括 classifier 专用路由和 Desktop 安全检测请求；普通对话使用 implicit 缓存，移除客户端标记，不生成内容块断点，避免每轮尾部标记迁移改变历史结构。原生协议请求保持透传。显式转换不用模型 ID 白名单拦截；Copilot 等第三方网关是否接受、实际是否命中需要观察上游，不能假定所有上游都会忽略不支持的字段。上游 400 保留原错误，不静默去掉字段后重试。

### classifier 显式转换与反向桥接规则

- Messages → Responses：system 中有显式断点时，保留每个文本块，转换为开头 developer 消息的 `input_text`，不再合并成顶层 `instructions`。user、轮次内 system/developer 文本块的断点也保留在原位置。工具定义及其顺序保持不变，属于后续 system 断点覆盖的前缀。
- `cache_control: {"type":"ephemeral"}` → `prompt_cache_breakpoint: {"mode":"explicit"}`。只有显式断点时，生成 `prompt_cache_options: {"mode":"explicit"}`；Anthropic 顶层自动 `cache_control` 转为 `mode: "implicit"`，可以和显式断点组合。
- Responses → Messages：前置 developer/system 文本块保留为 system 数组，并恢复 `cache_control`。`mode: "explicit"` 不额外增加自动尾部断点；显式指定 `implicit` 或只有块标记而未指定模式时，恢复 Anthropic 顶层自动缓存。完全没有缓存字段时维持原行为，不主动加断点。
- 工具结果断点位于整个结果末尾：Anthropic 外层 `tool_result.cache_control` ↔ `function_call_output.output` 数组最后一个非空 `input_text` 块的 `prompt_cache_breakpoint`，反向转换回外层，不能变成 Anthropic 不支持的嵌套断点。
- 目前只转换上述文本及工具结果结束边界。工具定义、tool/function call、图片/PDF、assistant 输出、thinking/replay 等没有已确认的对应位置，显式标记会明确报错，不伪造目标字段、不静默丢弃或移动断点。工具结果不是以非空文本结束、工具结果内部多个/非末尾标记也报错。
- 每请求最多四个断点（自动缓存占一个，若和最后一个显式断点位置及 TTL 相同则共用）；长 TTL 必须在短 TTL 之前；自动缓存与末尾已有断点 TTL 冲突时报错。未知模式、TTL、缓存预热及不可表示选项明确拒绝。
- TTL 按短/长档近似：Anthropic 默认/`5m` ↔ OpenAI 默认/`30m`，Anthropic `1h` ↔ OpenAI `12h`。**时长和计费不等价**，长档可能更贵；这不是保留原缓存存续时间或费用的保证。
- `prompt_cache_key` 只是上游路由/分组提示，不是缓存断点，不据此生成断点；跨协议转换不保证其分组语义。签名和搜索密文的 replay 保持原样，客户端不能向可见 assistant 回放项添加随后会被丢弃的断点。

### 分类器场景

两次独立请求分别为 `[固定规则, 操作A]` 和 `[固定规则, 操作B]`，在固定规则末尾放断点，使动态操作留在断点外；独立会话不妨碍同一上游缓存作用域内复用前缀。没有显式断点也不等于一定缓存不命中：旧 OpenAI 的隐式缓存可能复用共享前缀，但无法由调用方指定边界。

固定规则之前的工具、模型、提示词或其他影响缓存键的配置变化仍会导致未命中。发送了断点不代表一定写入或命中，还取决于模型支持、最小长度、前缀一致性、缓存作用域和有效期。JSON/SSE 缓存用量仍沿用现有转换；仅完成本地模拟验证，未验证真实 Copilot/Anthropic 的字段接受情况、缓存命中或费用。

参考：[OpenAI Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching) · [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
