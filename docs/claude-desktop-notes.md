本文档记录 Claude Desktop 第三方网关模式的模型识别行为。

- 黑名单关键词: gpt、openai、codex、gemini、deepseek、qwen、glm...
- 必须包含的关键词之一: claude、sonnet、opus、haiku、fable、mythos、anthropic
- 通过模型校验仅代表模型会可用，但是如果不匹配已知的自家模型，则无法展示**推理强度选择器**.
- 自家模型匹配规则: claude-(mythos|fable|sonnet|opus|haiku)-suffix 前缀，`claude-`可选, `-suffix`可选，中间必选。
- 自家模型 4.7 及以上版本支持五档Effort: low|medium|high|xhigh|max。 4.6模型只支持4档，不支持xhigh。4.5及以下模型只支持3档: low|medium|high。
- 自家模型 4.6 及以上版本均支持 1M context。
