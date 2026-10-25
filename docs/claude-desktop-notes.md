本文档记录 Claude Desktop (CCD) 第三方网关模式的模型识别行为。

参考文档：
- 3P Inference json配置Reference：https://claude.com/docs/third-party/claude-desktop/configuration

## 模型配置

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
- CCD可能仍展示上游不支持的档位。转发前，未支持的 `max` 降到实际最高档；未支持的 `xhigh` 降到 `high`，依次类推。
- 数字编码按账号和模型身份确定性生成，无需独立映射文件，并兼容旧编码请求。

## 其他特殊细节

- Tool按需加载功能默认仅在官方订阅生效，第三方订阅会默认加载全量tool。Gateway 生成配置包含 `toolSearchEnabled:true`；需 Apply 后重启，可手动设置 `false` 关闭。普通客户端工具搜索在跨协议时由网关转换，原生 Messages 保持透传。未实测 Desktop Chat/Cowork 各 surface 的独立 wire 行为，不能仅凭开关已生成就认定全部生效；[工具搜索桥接与限制](openai-to-anthropic-notes.md#客户端工具按需加载)。
- CCD Auto模式下, 会额外调用 LLM API 对请求进行安全分级，默认使用Sonnet模型且不可配置。可在LLM Gateway识别该类请求并配置性价比模型。(官方订阅的安全检查请求免费，第三方可能产生额外 token 费用)
