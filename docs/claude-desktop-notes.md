本文档记录 Claude Desktop 第三方网关模式的模型识别行为。

- 黑名单关键词: gpt、openai、codex、gemini、deepseek、qwen、glm...
- 必须包含的关键词之一: claude、sonnet、opus、haiku、fable、mythos、anthropic
- 通过模型校验仅代表模型会可用，但是如果不匹配已知的自家模型，则无法展示**推理强度选择器**. /v1/models API返回的effort无法控制实际展示的档位。
- 自家模型匹配规则: claude-(mythos|fable|sonnet|opus|haiku)-suffix 前缀，`claude-`可选, `-suffix`可选，中间必选。
- 自家模型 4.7 及以上版本支持五档Effort: low|medium|high|xhigh|max。 4.6模型只支持4档，不支持xhigh。4.5及以下模型只支持3档: low|medium|high。
- 自家模型 4.6 及以上版本均支持 1M context。

Copilot Chat 的 Desktop 专用模型 ID：

- 仅对使用 `copilotchat-claude-desktop` API key 的客户端生效；Claude Code 的模型 ID 不变。
- 原始 ID 含 `claude` 时，将 `.` 替换成 `-`，默认不加数字编码，例如 `claude-opus-4.6` → `claude-opus-4-6`。
- 在当前 Profile 中出现同名 Claude 模型时，按已保存的模型顺序，第一个保留上述名称，其余使用 `mythos-<数字编码>`。归一化后同名的 ID 也按重复处理。目录日期排序和分页不改变这个分配。
- 非 Claude 模型始终使用 `mythos-<数字编码>`，不再根据推理档位或窗口选择前缀。真实名称仍通过 `display_name` 展示，API 仍报告实际窗口和推理能力。
- Desktop 可能仍展示上游不支持的档位。转发前，未支持的 `max` 降到实际最高档；未支持的 `xhigh` 降到 `high`，依次类推。
- 数字编码由 Provider 内部 ID 与原始模型 ID 的 SHA-256 前 128 位转换为十进制得到；不同账号分别编码。无需保存独立映射文件，网关在当前 Profile 中匹配编码并还原上游路由；重启、Provider 改名和列表重排不会改变编码。
