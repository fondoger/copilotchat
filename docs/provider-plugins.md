# Provider 插件

打开 **Settings → Extensions → Provider plugins**。选择文件夹、ZIP，或填写 GitHub 仓库地址，然后点击 **Review and install…**。确认插件的来源、网络地址和登录权限后，在 **Settings → Providers → +** 添加该插件的账号。同一个插件可以添加多个账号，原生聊天和本地 LLM Gateway 使用各自绑定的账号。

| 来源 | 填法 | 更新时读取 |
| --- | --- | --- |
| 本地文件夹或 ZIP | 选择文件，或输入本机路径 | 重新读取原路径；运行中的代码来自已安装的快照 |
| GitHub | `owner/repo` 或 `https://github.com/owner/repo` | 默认分支，或填写的分支、tag、commit 对应的版本 |
| GitHub Enterprise | `https://github.example.com/owner/repo` | 使用已登录该主机且有读取权限的 GitHub CLI |

公共 GitHub 仓库优先匿名读取；私有仓库使用本机 `gh` 的登录状态。**Automatic updates** 按插件勾选，检查间隔使用 **General → Check frequency**。分支会跟踪新提交；需要可复现版本时填写完整 commit SHA。更新改变网络或 WAM 权限时，保留当前版本，请重新审核安装。

## 写一个插件

[最小示例](../examples/catalog-provider) 包含 `plugin.json` 和一个 ES 模块。示例使用保留域名，可用 `go test ./internal/plugin` 离线验证。部署时换成自己的服务地址。MoeJS 支持单文件 JavaScript 或 TypeScript；需要的库请先打包进入口文件。

```json
{
  "id": "example-catalog",
  "name": "Example model catalog",
  "version": "1.0.0",
  "main": "index.js",
  "network": ["https://models.example.org"]
}
```

入口导出三个同步函数，每次调用使用独立的 JS 环境：

- `catalog({version, callId, correlationId})` 返回 `{url, headers?, redirects?}`。宿主发送 GET；目录跳转需要同时匹配 `redirects` 中的完整 URL 和 `network` 中的 HTTPS origin。
- `models(body)` 将上游目录 JSON 转为模型数组。每项需要 `id`、`name` 和 `endpoint`。`picker: false` 保留按 ID 推理的能力并从选择器隐藏。对于含 `supported_endpoints`、`capabilities` 的目录，`host.model(entry, endpoint)` 使用宿主的共享转换器。
- `inference({catalogURL, endpoint, conversationId, agent, version, callId, correlationId})` 返回 `{url, headers?}`。`catalogURL` 是本账号最近一次成功发现的最终目录地址。宿主发送 POST；原生聊天使用共享协议编码器，Gateway 保留原始请求体、查询和流式响应。推理请求不跟随重定向。

`network` 使用小写、无端口的 HTTPS origin。插件可提供 `X-*` 和协议扩展头；宿主设置授权和内容类型。JS 不能读取文件、启动进程或直接发网络请求。

## Windows WAM 登录

需要工作账号的插件在 manifest 添加 `"wamProfile": "example-work"`。组织管理员或用户在插件目录之外创建配置文件，并将环境变量 `COPILOT_CHAT_WAM_PROFILES` 指向其**绝对路径**，再启动应用。例如：

```json
{
  "example-work": {
    "clientId": "<组织批准、属于本客户端的 Entra 应用 GUID>",
    "resource": "<该服务的 audience GUID>",
    "origins": ["https://models.example.org"]
  }
}
```

WAM 以 `api://<resource>/.default` 请求授权。所有插件网络地址都必须由该 profile 批准。Windows 负责账号选择和续期；系统凭据库只保存账号绑定。令牌由宿主在发送时附加，JS 不接触令牌。更换应用注册、资源或允许的地址后，重启并重新登录。macOS 可以使用无 WAM 的插件。
