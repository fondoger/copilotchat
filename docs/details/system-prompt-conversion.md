# 系统提示词与 `instructions` 转换

Anthropic Messages 使用顶层 `system` 传递系统提示词。OpenAI Responses 可以通过顶层 `instructions` 或 `input` 内的 `developer` 消息传递指令。Copilot Chat 最初把 `system` 映射为 `instructions`（提交 `d01e1d2`）；现在 Messages → Responses 桥接统一把它放在 `input` 开头，便于后续轮次复用提示词前缀。

## 转换规则

```json
{"system":[{"type":"text","text":"规则一"},{"type":"text","text":"规则二"}],"messages":[{"role":"user","content":"开始"}]}
```

转换后的 Responses 输入（省略其他字段）：

```json
{
  "input": [
    {"role":"developer","content":[{"type":"input_text","text":"规则一\n\n规则二"}]},
    {"role":"user","content":[{"type":"input_text","text":"开始"}]}
  ]
}
```

- 普通对话校验并移除客户端缓存标记。`system` 字符串原样使用；文本块数组按原顺序用两个换行拼接；结果为空字符串时省略前缀消息。
- 显式缓存路径同样把 `system` 放在开头的 `developer` 消息。有块级断点时，每个文本块单独转换，保留断点的位置和 TTL 映射，见[显式缓存转换](explict-cache-convert.md)。
- 历史中的 `system` / `developer` 消息留在原位置。工具定义、工具历史和加密 reasoning 继续按原规则转换。网关每轮使用客户端当前发送的 `system`；如果文本变化，新请求就用新版，提示词前缀也随之变化。
- 生成和 `/responses/input_tokens` token 计数复用同一提示词转换。规则适用于所有 Messages → Responses 路由；原生 Messages、原生 Responses 以及 Responses → Messages 的转换行为保持原样。

实现位于 [`convertMessagesRequest`](../../internal/localproxy/messages_request.go)；token 计数入口见 [`writeCountTokens`](../../internal/localproxy/count_tokens.go)。

## 为什么放在 `input` 开头

`instructions` 仍是有效的 Responses 参数。按 [OpenAI SDK 的定义](https://github.com/openai/openai-python/blob/main/src/openai/types/responses/response_create_params.py)，使用 `previous_response_id` 续接时，上一轮的 `instructions` 不会自动带入下一轮，调用方可以为当前请求重新指定规则。

Claude Code 2.1.327 默认会保存首轮系统提示词快照。本地用同一会话 ID 恢复时，默认继续发送原提示词；关闭 `--system-prompt-snapshot` 后，恢复请求采用新提示词。MCP 抓到的两个 Claude Desktop 2.1.326 会话分别有 5 条和 11 条成功请求，各自的 `system` 在采样期间保持一致。桥接无需识别哪一段文字是固定的，也无需自己保存快照。

Codex CLI 在提交 [`bb5b9c6`](https://github.com/openai/codex/commit/bb5b9c6240136de8ff4d30215e2341c44559aa04) 中也把基础提示词放进 [`input` 的 `developer` 消息](https://github.com/openai/codex/blob/bb5b9c6240136de8ff4d30215e2341c44559aa04/codex-rs/core/src/client.rs#L1267-L1298)，普通 Responses 请求没有顶层 `instructions`。这条规则不按模型版本区分。

## 缓存验证

2026-11-20，通过 Copilot Chat 的原生 Responses 路径向 GitHub Copilot 企业端点的 `gpt-6.2-sol` 发送了 14 个合成请求。每个场景用同一份约 4,000 token 的系统提示词对比两种放置位置；两组分别使用独立且组内稳定的会话 ID。MCP 核对了实际发往上游的请求和返回的用量。命中率按上游 `input_tokens_details.cached_tokens / input_tokens` 计算。

| 场景 | 顶层 `instructions` | `input` 首条 `developer` |
| --- | ---: | ---: |
| 长提示词、短对话，原样重发 | 0% | 99.9% |
| 长提示词、短对话，追加一轮 | 0% | 99.4% |
| 有历史和工具定义，原样重发 | 36.3% | 98.8% |
| 有历史和工具定义，追加一轮 | 36.2% | 98.5% |
| 有历史和工具定义，修改靠前的用户消息 | 0% | 67.2% |

有历史和工具定义时，两组首轮输入均为 6,319 token；上游缓存写入分别为 2,295 和 6,244 token。追加一轮时，未命中输入从 4,044 降至 95 token。短对话场景下，顶层 `instructions` 一组的缓存读写均为 0；有较长历史时，该组仍可以命中历史前缀。

排查时看上游原始用量：读取量为 `input_tokens_details.cached_tokens`，写入量为 `input_tokens_details.cache_write_tokens`（上游提供时）。当前 Responses → Messages 用量桥接把 `cache_creation_input_tokens` 填为 0。以上数据只覆盖该上游和模型的隐式缓存，未测其他上游，也未比较回答质量。
