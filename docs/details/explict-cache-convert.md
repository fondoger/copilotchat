# 显式缓存转换

Anthropic Messages 用 `cache_control` 标记缓存断点。OpenAI Responses 在 GPT-5.6 及之后的模型上支持 `prompt_cache_options` 和内容块级的 `prompt_cache_breakpoint`；较早的 OpenAI 模型使用隐式缓存。

## 适用范围

网关仅对 Auto 模式安全分类器（classifier）的 Messages → Responses 请求转换显式断点，涵盖 classifier 专用路由和 Desktop 安全检测请求。

普通对话使用隐式缓存：转换时移除客户端缓存标记，不生成内容块断点，保持历史结构稳定。原生协议请求透传。

普通对话先校验缓存标记的格式和 TTL 顺序，再移除标记；工具定义、assistant 文本、工具调用和图片等位置均按此处理。Claude 自动 compact 请求携带的历史断点也走这条路径。工具参数和 JSON Schema 内名为 `cache_control` 的业务字段保持原样。

显式转换不设模型 ID 白名单。Copilot 等第三方网关对字段的支持情况需要实测；上游返回 400 时，网关保留原错误，不移除字段重试。

Claude Desktop会在执行会话压缩时，会在压缩前的最后一条消息上加上显式断点，提示后续消息不用再缓存。目前的实现方式会将压缩视作普通会话，移除所有显式断点。

## 分类器示例

两次独立请求共用固定规则，各自携带不同操作：

```text
请求 A：[固定规则 | 缓存断点] [操作 A]
请求 B：[固定规则 | 缓存断点] [操作 B]
```

断点放在固定规则末尾，动态操作位于断点之后。同一上游缓存作用域内，两个请求可以复用固定前缀。隐式缓存也可能复用共享前缀，其边界由上游决定。

## Messages → Responses

system 中有显式断点时，每个文本块分别转换为开头 developer 消息的 `input_text`，保留原有块边界和断点位置。user 和轮次内 system/developer 文本块上的断点同样保留在原位置。

工具定义及其顺序保持不变，纳入后续 system 断点覆盖的前缀。

| Messages 字段 | Responses 字段 |
| --- | --- |
| 块级 `cache_control: {"type":"ephemeral"}` | 块级 `prompt_cache_breakpoint: {"mode":"explicit"}` |
| 仅有显式断点 | 顶层 `prompt_cache_options: {"mode":"explicit"}` |
| 顶层自动 `cache_control` | 顶层 `prompt_cache_options: {"mode":"implicit"}`，可同时保留块级显式断点 |

> **工具结果断点**
> 
> Anthropic 外层 `tool_result.cache_control` 转换到 `function_call_output.output` 数组最后一个非空 `input_text` 块的 `prompt_cache_breakpoint`，表示整个工具结果的结束边界。
> 
> 工具结果必须以非空文本结束。工具结果内部的多个标记或非末尾标记会报错。

## Responses → Messages

前置 developer/system 文本块转换为 system 数组，并恢复块级 `cache_control`。

| Responses 缓存配置 | Messages 转换结果 |
| --- | --- |
| `mode: "explicit"` | 恢复显式断点 |
| `mode: "implicit"` | 恢复顶层自动缓存，并保留已有显式断点 |
| 只有块标记，未指定模式 | 恢复块级断点和顶层自动缓存 |
| 没有缓存字段 | 维持原行为 |

工具结果末尾的断点恢复到外层 `tool_result.cache_control`。

## 显式断点与 TTL 限制

显式转换时，每个请求最多四个断点，自动缓存占一个。自动缓存与最后一个显式断点的位置、TTL 均相同时，共用一个断点；位置相同但 TTL 冲突时报错。长 TTL 断点必须排在短 TTL 断点之前。

TTL 按短、长两档近似映射：

| 档位 | Anthropic | OpenAI |
| --- | --- | --- |
| 短 | 默认或 `5m` | 默认或 `30m` |
| 长 | `1h` | `12h` |

转换后的存续时间和计费按目标上游规则执行，长档可能产生更高费用。

目前支持的断点位置为上述文本块及工具结果结束边界。以下输入会报错：

- 工具定义、tool/function call、图片/PDF、assistant 输出、thinking/replay 上的显式标记。
- 未知模式、未知 TTL、缓存预热及其他无法表示的缓存选项。

`prompt_cache_key` 仅用于上游路由或分组提示，不据此生成断点；跨协议转换时分组语义取决于目标上游。签名和搜索密文的 replay 保持原样；客户端向可见 assistant 回放项添加断点会报错。

## 命中条件与验证状态

缓存命中取决于模型支持、最小前缀长度、前缀一致性、缓存作用域和有效期。固定规则之前的工具、模型、提示词或其他影响缓存键的配置发生变化，仍会导致未命中。

JSON/SSE 缓存用量沿用现有转换。当前已完成本地模拟验证；真实 Copilot/Anthropic 上游的字段接受情况、缓存命中和费用尚未验证。

## 参考

- [OpenAI Prompt caching](https://developers.openai.com/api/docs/guides/prompt-caching)
- [Anthropic Prompt caching](https://platform.claude.com/docs/en/build-with-claude/prompt-caching)
