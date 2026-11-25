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

支持交互登录的宿主提供 `host.apiVersion`（当前为 `2`）。需登录的插件应在返回请求前检查版本：旧宿主看不懂新的认证声明时，提示升级并停止请求。

在 `plugin.json` 中用 `wamResource` 声明模型服务的 Entra API audience（非零 GUID），用 `network` 列出目录与推理所需的 HTTPS 地址。入口模块再导出一个同步函数：

```js
export function signIn() {
  return host.wam.signIn();
}
```

用户点击 **Sign in** 时，宿主调用 `signIn`。`host.wam.signIn()` 不接受参数，返回本次登录请求；宿主在函数成功返回该请求后打开 Windows 账号选择器。模块初始化、目录刷新、推理和自动更新都无法调用这个能力。

**首次配置账号：** 在 **Settings → Providers** 选择插件账号，按服务管理员提供的信息填写应用的 Application (client) ID、API resource GUID 和允许接收令牌的 HTTPS origins，保存注册后点击 **Sign in**。这些值由用户在宿主中授权。宿主比对插件声明的 audience 和网络地址后，为 `api://<resource>/.default` 请求授权。同一账号后续启动可恢复绑定并静默续期。

系统凭据库保存账号绑定，JS 接触不到账号引用或令牌。更换应用、资源或获准的地址后需重新登录。macOS 可继续使用无需 WAM 的插件。
