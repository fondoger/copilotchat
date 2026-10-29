# Tool Search 流式兼容与缓存前缀

本文记录 2026-10-29 针对 GitHub Copilot Responses 上游的兼容修复，涵盖流式 item ID 变化、历史重放和缓存前缀。搜索协议与按需加载流程见[工具搜索与按需加载](tool-search-on-demand-loading.md)。

## 问题现象

Claude Code／Desktop 可能提示：

```text
API Error: Server error mid-response. The response above may be incomplete.
```

捕获的请求为 HTTP 200，上游已经返回 `response.completed`，状态为 `completed`、`error:null`。网关随后发出 SSE 错误，缺少正常的 `message_stop`：

```text
Responses terminal item differs from streamed item
```

工具块可能已完整发给客户端，因此客户端仍能执行工具并继续下一轮；流的结束过程仍然失败。

## 根因：同一输出项的 ID 会变化

Copilot 会在不同事件中重新生成根级 `item.id`。同一个 `output_index` 对应的 Tool Search 调用可以出现以下变化：

| 字段 | `response.output_item.done` | `response.completed` 中的输出项 |
| --- | --- | --- |
| `id` | `ts_done` | `ts_terminal` |
| `call_id` | `call_123` | `call_123` |
| `type` | `tool_search_call` | `tool_search_call` |
| `execution` | `client` | `client` |
| `arguments` | `{"query":"read"}` | `{"query":"read"}` |
| `status` | `completed` | `completed` |

原实现对整个对象做相等比较，根级 ID 的变化触发了流末尾错误。此前真实上游对照中，普通文本流 2/2、普通函数调用流 2/2 正常，自动选择的原生客户端 Tool Search 流 3/3 复现错误；同类非流式搜索正常。

## 流式对齐规则

原生搜索项和带 namespace 的 `function_call` 按 `output_index` 对齐：

- 去重时忽略根级 `id`，其余字段继续完整比较，包括 `call_id`、参数、execution、namespace、状态、工具结果和未知扩展字段。
- 重复的 done／终态项通过校验后直接跳过，保持已经发出的内容块及其顺序。
- 终态项先与已完成项比较，再按类型分派，防止类型变化绕过一致性校验。
- 服务端搜索先暂存 call，待相邻 output 完成后一起生成回放封装。等待期间重复收到同一 call，也允许根级 ID 变化并保留首次数据。

## 哪些 ID 会进入下一轮

`item.id` 与 `call_id` 的用途不同：前者标识 Responses 输出项，后者关联客户端工具调用与结果。

| 内容类型 | 发给 Claude 的形式 | 下一轮发给 Responses 的内容 |
| --- | --- | --- |
| 客户端 ToolSearch | `tool_use.id = call_id`，输入为搜索参数 | 重建 `tool_search_call` 与 `tool_search_output`，保留 `call_id` 和参数，省略根级 `id` |
| 服务端搜索、带 namespace 的函数调用 | 可见工具块及 `copilotchat-tool-search-v1:` 回放签名 | 从签名恢复完整原生项，保留首次完成项的原始 ID 和元数据 |
| reasoning | thinking 块及 `copilotchat-responses:` 回放签名 | 从签名恢复已发送的原生 reasoning，包括 ID、加密内容和扩展字段 |

Claude 客户端原样回传回放签名。终态响应重新生成的 ID 不覆盖已发送的签名，也不改写后续重放使用的历史。

## 工具目录与缓存前缀

在工具定义和系统指令保持一致时，新增对话轮次应只追加上游 `input`，已有部分的序列化结果保持不变：

- 客户端发现的 schema 放在历史 `tool_search_output.tools` 中，从顶层 `tools` 移除已发现定义，保持常驻工具目录稳定。
- 已发出的 reasoning、原生搜索和 namespace 回放数据保持原样，包括工具调用 ID、签名、加密内容、原生项顺序及大整数精度。
- 普通对话校验并移除客户端缓存标记，使用隐式缓存；客户端移动断点不会改变已有消息的上游表示。分类器的显式缓存规则见[显式缓存转换](explict-cache-convert.md)。

回归测试覆盖客户端搜索、服务端搜索和 namespace 函数调用：模拟 added、done、重复 done、终态中的根级 ID 变化，将实际生成的 Messages SSE 拼回历史，再连续追加三轮，检查历史字节前缀、工具目录和 instructions 保持稳定。

## 强制选择原生搜索的限制

本次 Copilot 上游实测中，指定 `tool_choice:{"type":"tool_search"}`、通过 `allowed_tools` 强制原生搜索，以及仅有原生搜索时使用 `required` 均返回 400。

Messages 桥接在本地拒绝以下无法表达的选择：

- 按名称强制调用原生搜索入口，包括客户端 `ToolSearch` 和服务端 regex／BM25 搜索。
- 转换后的工具表只有原生搜索时，使用 `tool_choice:{"type":"any"}`。

错误提示为：

```text
native tool search does not support forced tool_choice; use auto or omit tool_choice
```

自动选择、禁用工具及普通业务函数的指定选择保持各自语义。搜索协议和顶层工具目录在不同工具选择下保持一致。非 GPT 上游的普通 function 搜索兼容路径沿用原有行为。

## 真实缓存验证

2026-10-29 使用当前源码的请求／响应转换器，经已安装 Gateway 的原生 Responses 路径访问 GitHub Copilot `gpt-6-astra`。同一账号、同一诊断 session 连续发送六次请求，固定系统前缀约四千 tokens；搜索结果携带一份较长的工具 schema。客户端缓存标记随轮次移动，转换后保持隐式缓存。

| 场景 | 上游 `input_tokens` | 上游 `cached_tokens` | 未命中输入 |
| --- | ---: | ---: | ---: |
| 首次请求 Tool Search | 4,293 | 0 | 4,293 |
| 原样重复请求 | 4,293 | 4,290 | 3 |
| 带回工具发现结果，继续对话 | 6,114 | 4,290 | 1,824 |
| 原样重复这轮请求 | 6,114 | 6,111 | 3 |
| 再追加一轮对话 | 6,137 | 6,111 | 26 |
| 修改前缀开头，作对照 | 6,163 | 0 | 6,163 |

六次请求均正常结束，每次都观察到 done 与终态之间的输出项 ID 变化。工具发现后仍命中原有前缀，新增历史进入缓存后命中量增长；修改前缀开头使缓存读取归零。追加轮次的计数已通过 GoVisual 捕获的 Provider 原始响应交叉核对。

Messages 用量按以下口径转换：

```text
cache_read_input_tokens = 上游 input_tokens_details.cached_tokens
input_tokens = 上游 input_tokens - cache_read_input_tokens
```

本次六组缓存读取量均准确对应。上游还返回 `cache_write_tokens`；当前该部分计入普通输入，`cache_creation_input_tokens` 为 0。

本次真实请求覆盖客户端 ToolSearch、结果续接和隐式缓存。服务端搜索、namespace 及 reasoning 重放在这次修复中的 ID 变化与多轮前缀稳定性由离线回归覆盖。尚未在更新后的真实 Claude Code／Desktop 界面完成端到端验证，测试未执行模型请求的业务工具。

## 实现与回归入口

- [流式输出与终态校验](../../internal/localproxy/messages_response.go)
- [搜索去重与回放封装](../../internal/localproxy/messages_search_response.go)
- [搜索历史和工具选择转换](../../internal/localproxy/messages_search_request.go)
- [流式重放及多轮缓存前缀测试](../../internal/localproxy/messages_search_stream_test.go)
- [响应边界测试](../../internal/localproxy/messages_search_response_test.go)与[工具选择测试](../../internal/localproxy/messages_search_request_test.go)
