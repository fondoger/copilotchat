Go/MyGo；Windows/macOS、多 Provider、Agent LLM Gateway、原生/网页聊天。

## 约定

- 默认main分支开发；UI 靠布局表达，少副标题/说明。
- 测试仅覆盖有意义行为；不测琐碎逻辑、文案、布局、实现细节。
- 结构/命名/文件组织随需求重构，勿绕补；大改先提方案。

## 开发

- `go run .`：隔离 Dev；Gateway 39322(debug)/39321(stable)。
- `make dev`：替换本机安装版并重启，复用其数据/账号/端口。

## 架构

- UI 状态单线程；worker→app.update/Window.Update；身份+generation 防陈旧回写。
- 原生：internal/desktop/chat.go→provider.Engine；InferenceBackend 管账号/鉴权。Gateway：provider.RawClient→internal/localproxy，raw body/SSE，按需 Messages↔Responses。
- 协议历史：保留 tool ID、原生顺序、签名/加密 reasoning、数值精度、缓存前缀。
- WebView 网页推理/持久登录；生成 HTML/SVG 预览独立 opaque-origin 沙箱，禁网络/native bridge。
- internal/store 原子快照；上游凭据 OS secrets；LocalKey=Gateway 密钥。
- MCP：internal/mcp=聊天工具，执行前审批；internal/localproxy/inspection.go=GoVisual 诊断。

## 参考

- 协议：docs/openai-to-anthropic-notes.md、docs/details/；客户端差异：docs/*-notes.md。
- reference/：Claude Code/Codex、Magpie/cc-switch；文档风格：docs/AGENTS.md。
- 使用copilot-chat-incoming-request-debugger可调试请求。
