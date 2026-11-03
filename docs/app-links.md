# 应用内操作链接

原生回复正文和错误信息都使用 Markdown 渲染，可包含应用内操作链接：

```markdown
请先登录豆包。[打开豆包](copilotchat://doubao/open)
```

链接在用户点击后执行。仅渲染、接收或流式输出链接不会触发操作。

## 当前路由

| 链接 | 操作 |
| --- | --- |
| `copilotchat://doubao/open` | 打开这条消息所属豆包 Provider 的设置登录页 |

豆包适配器的登录/验证错误将链接放在普通 `message` 中，HTTP、SSE 和本地历史继续传递这段文本。打开登录页时校验 Provider 仍存在、已启用且为豆包；没有消息级 Provider 时使用会话的 Provider。完成后点击 Done 释放页面，再手动重试。

## 分发与边界

`internal/desktop/message_links.go` 通过 MyGo Markdown 的 `OnLink` 处理点击，`appLinkAction` 集中登记应用内动作。当前路由接受表中的精确地址，其他路径、query 和 fragment 不执行。普通外部链接继续使用 MyGo 的 `ui.SafeToOpen` 检查，只允许 HTTP、HTTPS 和 mailto。

此方案仅在原生消息 Markdown 中分发，没有注册系统 URL scheme。WebView 和生成预览仍使用各自的导航/沙箱策略，不连接这个操作路由。

后续增加 UI 入口或其他操作时，在 `appLinkAction` 登记路由、校验目标和参数；涉及状态变更或系统能力的操作需在处理器中加入适当的确认或审批，并补充拒绝未知/失效目标的测试。AI 可以输出已支持的 Markdown 链接，执行仍由用户点击触发。
