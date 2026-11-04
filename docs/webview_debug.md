# WebView 调试

先加载 `agent-browser` skill。Windows 下，在仓库根目录用 Git Bash 启动带 WebView2 调试端口的应用：

```bash
WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS='--remote-debugging-port=9223' go run .
```

在应用中打开网页 Provider 或 Open Doubao，创建 WebView 后连接：

```bash
agent-browser --session webview-debug connect 9223
```

将 `webview-debug` 替换为本次任务的独立会话名。连接后按页面 URL 选择目标，后续操作遵循 `agent-browser` skill。

共享同一 Profile 的 WebView2 可能复用已有浏览器进程；端口未开启时，先退出对应应用实例，再带参数重启。

调试端口仅在本机临时开启，结束后退出调试实例。
