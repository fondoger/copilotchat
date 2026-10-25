本文档记录CC(Claude Code CLI/Claude Desktop)的客户端行为。

参考文档：
- settings.json 完整配置项: https://code.claude.com/docs/zh-CN/settings-reference#availablemodels
- 自动压缩文档：https://code.claude.com/docs/zh-CN/model-config#context-window-and-auto-compaction

Notes:
- Fable、Sonnet 5、Opus 4.7等更高版本的Claude模型，默认窗口为1M，可以通过CLAUDE_CODE_DISABLE_1M_CONTEXT=1限制CC只使用200K窗口。
- `_SUPPORTED_CAPABILITIES` 环境变量可以为第三方模型提供thinking effort选项。
- `modelPicker` 配置可以在4种(Opus/Sonnet/Haiku/Fable)模型之外，额外列出第三方模型。甚至可以让不满足自家模型命名规则的模型id `claude-opus-4.6` behaveAs `claude-opus-4-6`。
- `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`启用模型自动发现后，CC才会访问 `/v1/models` API，并会剔除模型ID中不包含 `anthropic`/`claude`的模型。
- 对未知模型，在model ID加上 `[1m]` 后缀，可以让CC使用1M的Context Window，eg: `gpt-5.5[1m]`
- CC不支持为**未知模型**指定Context Window大小。所有未知模型只能使用 200K(默认) 或者 1M (通过`[1m]`后缀开启)。使用 `[1m]`后缀 + per model的`autoCompactWindow`参数，可以控制实际使用的Context Window大小。
- 优先使用 modelPicker + replaceBuiltInOptions 配置模型选择器。
- 启用Auto权限模式时，CC会额外调用LLM API对请求安全分级，可通过`CLAUDE_CODE_AUTO_MODE_MODEL`。(官方订阅的安全检查请求免费，第三方可能产生额外 token 费用)
