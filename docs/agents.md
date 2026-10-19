# Claude Code / Codex 配置

在 **Settings → Extensions → Agent setup** 中选择已登录的 Provider、Agent 和模型，然后点击 **Copy configuration**。

- **Claude Code**：将生成的字段合并到 `~/.claude/settings.json`，清除冲突的 `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_API_KEY` 设置，然后重启 Claude Code。生成的 Opus、Sonnet、Haiku 和辅助模型别名均指向所选模型。
- **Codex / ChatGPT Desktop**：将顶层模型设置和 Provider 表合并到 `~/.codex/config.toml`，替换同名条目，不要重复追加，然后重启客户端。这里的 ChatGPT Desktop 指支持 Codex 配置的桌面编程应用。需要支持 Provider `auth.command` 的客户端版本；不要在同一 Provider 中同时配置 `env_key`、`experimental_bearer_token` 或 `requires_openai_auth`。

配置页只列出该 Provider 已启用且协议兼容的模型：Claude Code 使用 Messages，Codex 使用 Responses。仅支持 Messages 的 Claude 模型不会出现在 Codex 选项中。没有兼容模型时可以刷新模型目录。高级客户端功能仍取决于远端服务的支持，例如特定 beta 功能和远端压缩。

## 内置凭据 helper

Windows 安装目录包含 `copilot-chat-api-key-helper.exe`；macOS 的应用包 `Contents/Resources` 中包含 `copilot-chat-api-key-helper`。配置页生成对应平台的绝对路径。移动应用或更换 Provider 账号后需要重新生成配置，正常原位更新无需重新配置。

helper 直接读取 Copilot Chat 的系统凭据库，绑定配置中的 Provider ID，可在主程序关闭时使用。GitHub 使用该 Provider 的登录令牌；Microsoft 365 复用该 Provider 的 WAM 或 Copilot CLI 来源并静默续期。它不会切换到别的账号，也不会发起交互式登录。需要重新授权时，在 Copilot Chat 的 Providers 页面处理。

普通调用成功时仅向 stdout 输出一行令牌；错误写入 stderr 并返回非零状态。配置中没有静态令牌，helper 不会把令牌写到文件。默认每五分钟由 Agent 再次调用。禁用、退出或删除 Provider 后，后续 helper 调用失败；Agent 已缓存的令牌可能在刷新前继续使用，立即停用时也应关闭相应 Agent 会话。

可在终端检查登录而不显示令牌：

```powershell
& 'C:\path\to\copilot-chat-api-key-helper.exe' --provider '<provider-id>' --check
```

如果应用使用自定义数据目录，需传入 `--data-dir '<directory>'`；配置页会自动包含这一参数。Windows 的 Claude Code 命令使用系统 PowerShell 编码参数，兼容路径中的空格、中文和 shell 特殊字符。

## 开发与验证

`scripts/build.ps1` 同时构建 Windows 应用和 helper。单独构建可使用 `pwsh ./scripts/build-helper.ps1 -Platform windows/amd64 -OutputDirectory bin`。打包脚本先构建对应平台的 helper，再交给 MyGo 签名并生成安装包和更新包。

已验证 Windows 独立 helper 对 GitHub 和 M365 的取令牌流程、配置生成、账号隔离、命令转义和原生配置页面。未据此声明所有 Claude Code / Codex 功能均已完成实机端到端测试。

配置格式依据 [Claude Code apiKeyHelper](https://code.claude.com/docs/en/settings-reference#apikeyhelper)、[Claude Code 网关协议](https://code.claude.com/docs/en/llm-gateway-protocol)及 [Codex 自定义模型 Provider](https://learn.chatgpt.com/docs/config-file/config-advanced#custom-model-providers)。
