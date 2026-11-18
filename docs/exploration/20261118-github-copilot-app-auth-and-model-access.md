# 自建 GitHub App 与 OAuth App 调用 Copilot：授权、token 生命周期与模型访问

整理日期：2026-11-18。实测日期：2026-11-11、2026-11-18。

目标：GUI 客户端允许用户登录自己的 GitHub 账号，使用该用户的 Copilot 访问资格调用模型，并使用自己的应用身份完成授权。

## 结论

- 自建 GitHub App 获取的用户 token 可以直接调用部分 Copilot 模型。本次 `gpt-4o-mini` 请求返回 HTTP 200，模型回复 `OK.`。
- 新注册的 OAuth App 使用 `read:user` 完成登录，用户资料、Copilot 资格和模型目录查询均返回 HTTP 200。
- 同一账号通过两种自建应用登录后，服务端路由和返回的 8 个模型相同。`gpt-6-astra` 均未出现在目录中，直接请求 Responses 接口均返回 HTTP 400、`model_not_supported`。本次更换应用注册类型没有改变访问范围，限制来源尚未确定。
- 自建 GitHub App 签发的是 `ghu_` 用户 token，`expires_in=28800`，即 8 小时，并附带 refresh token。2026-11-18 已实测刷新成功。
- 自建 OAuth App 签发的是 `gho_` token，`scope=read:user`，未返回 `expires_in` 或 refresh token，符合长效 OAuth token 的表现。
- 当前正式客户端仍复用 OpenCode 的 OAuth App，只解析、保存和复用 access token，没有实现 token 刷新。原登录响应未保存过期信息，无法还原那枚 token 的准确有效期。
- 本次没有读取自建 GitHub App 后台的最终权限配置，无法据此给出已验证的最小权限清单。它返回的空 `scope` 不能解释为“应用没有任何权限”。

## 应用身份与当前实现

| 项目 | 当前正式客户端 | 自建 GitHub App 测试 | 自建 OAuth App 测试 |
| --- | --- | --- | --- |
| 应用类型 | OpenCode 使用的 OAuth App | GitHub App | OAuth App |
| Client ID | `Ov23li8tweQw6odWQebz` | `Iv23lizu0pvC2nu0bFZm` | `Ov23liLsec2FoZb7FE3Y` |
| 登录方式 | Device Flow | Device Flow | Device Flow |
| 登录请求中的 OAuth scope | `read:user` | 未发送 `scope` | `read:user` |
| 用户凭据 | 仅保存 access token 字符串 | 保存 access token、refresh token 和过期信息 | 保存 token 响应；无过期字段或 refresh token |
| 自动刷新 | 未实现 | 测试程序未实现；另行执行过一次刷新请求 | 未执行刷新 |

Client ID 是公开的应用标识。本文不记录 access token、refresh token、device code 或 Client Secret。

当前代码位置：

- [client.go](../../internal/provider/github/client.go)：硬编码 OAuth Client ID，保存当前 token 和服务端路由。
- [auth.go](../../internal/provider/github/auth.go)：申请设备码、轮询登录结果；响应结构只解析 `access_token` 及错误、轮询间隔字段。
- [providers.go](../../internal/desktop/providers.go)：将 access token 保存到系统凭据库；启动时取回同一个 token 创建客户端。
- [inference.go](../../internal/provider/github/inference.go)：推理请求直接携带用户 token。
- [现有 Copilot 协议说明](../copilot.md)：模型目录、路由、协议和请求头约定。

OpenCode 当前公开源码也使用上述 OAuth Client ID，并在设备授权请求中发送 `scope: "read:user"`。这只能证明其公开登录实现；应用后台配置和额外服务端授权未公开验证。

## 权限：能确认什么

### OAuth App

OAuth App 通过登录请求中的 `scope` 参数申请权限。`read:user` 的官方含义是读取用户个人资料。

2026-11-18，新注册的 OAuth App 开启 Device Flow，使用 `read:user` 完成实测。GitHub 用户资料和 Copilot 资格接口的响应头均返回 `X-OAuth-Scopes: read:user`，两个接口及模型目录查询均为 HTTP 200。向 `gpt-6-astra` 发起的推理请求返回模型不受支持；本次未用该 OAuth App 重复测试 `gpt-4o-mini`。

### GitHub App

GitHub App 的用户访问受应用权限和用户自身权限共同约束。本次自建 GitHub App 采用用户授权流程，拿到 `ghu_` token；未发送 OAuth `scope` 参数，token 响应中的 `scope` 为空。

注册页中与此次讨论相关的权限：

| 权限 | 官方描述或页面用途 | 本次调研边界 |
| --- | --- | --- |
| Organization → Copilot Requests | 允许应用发起由安装所在组织承担费用的 Copilot 请求 | 官方另有安装 token 的服务端接入流程；本次使用用户 token |
| Account → Copilot Chat | Copilot Extensions 接收该扩展相关的聊天消息及时间信息 | 该说明不能证明它授予通用模型推理权限 |
| Account → Copilot Editor Context | 扩展接收编辑器上下文 | 本次没有测试扩展功能 |
| Account → Models | GitHub Models 访问 | 该权限描述不能证明它授予 Copilot 订阅模型访问 |

GitHub `/user` 在实测中返回了：

```text
X-Accepted-GitHub-Permissions: allows_permissionless_access=true
```

这描述的是 `/user` 端点的访问要求。Copilot 模型接口的最小权限不能从这个响应头推出。

本次没有取得自建应用后台的最终权限清单，也没有进行逐项增减权限的对照实验。此前“GitHub App 全部不选权限即可调用”的判断缺乏依据。

## 实测方法

GitHub App 使用独立 Go 测试程序；新增的 OAuth App 测试使用仓库外的临时 PowerShell 脚本。两者均通过标准 HTTP 客户端调用接口，正式客户端的登录和推理实现未修改。

1. 向 `https://github.com/login/device/code` 发送相应应用的 Client ID。GitHub App 不发送 `scope`；OAuth App 发送 `scope=read:user`。
2. 用户在 GitHub 页面输入设备码，检查应用身份与权限后完成授权。
3. 向 `https://github.com/login/oauth/access_token` 轮询，发送 `client_id`、`device_code` 和 `grant_type=urn:ietf:params:oauth:grant-type:device_code`。
4. 将用户凭据及响应中存在的过期信息保存到仓库外的临时 JSON。两次设备授权和 GitHub App 的刷新均未使用 Client Secret。
5. 查询用户资料和 Copilot 资格，根据服务端返回的 `endpoints.api` 选择 Copilot 路由。
6. 查询模型目录，发送一次最小推理请求：`Reply with exactly OK.`。

用户 token 直接用于 Copilot 请求。本次没有经过旧的 `/copilot_internal/v2/token` 交换，也没有使用 GitHub App 安装 token。

版本与请求头沿用当前项目的约定：

| 请求 | `X-GitHub-Api-Version` |
| --- | --- |
| GitHub `/user` | `2022-11-28` |
| GitHub `/copilot_internal/user` | `2025-04-01` |
| Copilot 模型目录与推理 | `2026-08-01` |

推理请求携带 `Authorization: Bearer <user-access-token>`、应用自己的 User-Agent、`Openai-Intent: conversation-edits`、`X-Interaction-Type: conversation-agent` 和 `x-initiator: user`。

## 模型访问结果

两种自建应用均使用同一 GitHub 账号，沿用相同的 API 版本与请求头约定，并均被服务端路由到 `https://api.enterprise.githubcopilot.com`。本次没有核验订阅套餐名称或实际账单归属。

| 请求 | GitHub App，2026-11-11 | GitHub App，2026-11-18 刷新后 | OAuth App，2026-11-18 |
| --- | --- | --- | --- |
| `GET https://api.github.com/user` | HTTP 200 | HTTP 200 | HTTP 200 |
| `GET https://api.github.com/copilot_internal/user` | HTTP 200，`chat_enabled=true` | HTTP 200，`chat_enabled=true` | HTTP 200，`chat_enabled=true` |
| `GET https://api.enterprise.githubcopilot.com/models` | HTTP 200 | HTTP 200 | HTTP 200 |
| `POST /chat/completions`，`model=gpt-4o-mini` | HTTP 200，回复 `OK.` | 未重复测试 | 未测试 |
| `POST /responses`，`model=gpt-6-astra` | 未测试 | HTTP 400，`model_not_supported` | HTTP 400，`model_not_supported` |

两种自建应用返回的模型目录相同，包含以下 8 个 ID：

```json
[
  "gpt-4o-mini-2024-07-18",
  "gpt-4o-2024-11-20",
  "gpt-4o-2024-08-06",
  "gpt-41-copilot",
  "gpt-3.5-turbo-0613",
  "gpt-3.5-turbo",
  "gpt-4o-mini",
  "gpt-4o"
]
```

`gpt-6-astra` 在两种自建应用下均返回以下错误：

```json
{
  "error": {
    "message": "The requested model is not supported.",
    "code": "model_not_supported",
    "param": "model",
    "type": "invalid_request_error"
  }
}
```

此前曾误用 `gpt-6.1-astra` 测试，后由用户更正为 `gpt-6-astra`。误用名称的报错不用于判断正确模型的访问能力。测试程序也修正了目录外 GPT-6 模型的路由，正确模型的最终结果来自 `/responses` 请求。

本次从 GitHub App 换成 OAuth App，模型目录与目标模型报错没有变化。应用身份的服务端准入、账号或组织策略、模型 ID 或别名中，哪一项导致拒绝，尚未区分；同账号通过现有 OpenCode OAuth 登录后的模型访问范围未做对照。

## Token 生命周期与刷新

### 正式客户端为何可以长期保持登录

GitHub 官方文档说明：OAuth App 默认签发长效 token，也可以配置会过期的 token；GitHub App 用户 token 默认 8 小时过期，应用所有者可以调整相关设置。

因此，token 的实际有效期要看签发配置与响应中的 `expires_in`。应用类型和 token 前缀不足以单独确定有效期。

当前客户端只保存并复用原 token。系统凭据库负责存储，代码中没有刷新逻辑。原 token 长期可用，符合长效凭据的行为；撤销授权或其他安全规则仍可使其失效。

### 本次自建 GitHub App 的实际响应

| 字段或特征 | 观测值 |
| --- | --- |
| access token 前缀 | `ghu_` |
| `scope` | 空字符串 |
| `expires_in` | `28800`，8 小时 |
| `refresh_token` | 存在，值未输出到诊断日志 |

2026-11-18 复测时，2026-11-11 保存的旧 access token 已失效：GitHub 用户接口返回 HTTP 401、`Bad credentials`，Copilot 接口返回 `credential expired`。

随后向 `https://github.com/login/oauth/access_token` 发送刷新请求：

```text
client_id=<自建应用的 Client ID>
grant_type=refresh_token
refresh_token=<已保存的 refresh token>
```

刷新成功，返回新 access token、新 refresh token，新的 `expires_in` 为 `28800`。新凭据保存到新的临时目录后，GitHub 用户、Copilot 资格和模型目录查询恢复 HTTP 200；`gpt-6-astra` 仍返回模型不受支持。

GitHub 文档说明，设备授权签发的用户凭据在刷新时无需 Client Secret；刷新会轮换 access token 和 refresh token。正式接入需要安全持久化新的凭据对，并处理刷新失败和重新授权。本次仅验证刷新请求，未修改正式客户端的凭据结构或生命周期管理。

### 本次自建 OAuth App 的实际响应

| 字段或特征 | 观测值 |
| --- | --- |
| access token 前缀 | `gho_` |
| `scope` | `read:user` |
| `expires_in` | 未返回 |
| `refresh_token` | 未返回 |

该响应符合长效 OAuth token 的表现。本次没有进行长期持续有效性测试，授权撤销和安全规则仍可能使 token 失效。两种自建应用的 token 生命周期表现不同，模型目录与 `gpt-6-astra` 的拒绝结果相同。

## 官方支持范围与本次实测的关系

GitHub 的 Copilot SDK OAuth 指南支持以下流程：用户授权自己的 OAuth App 或 GitHub App，将用户 token 交给 SDK，使用该用户的 Copilot 订阅。

该指南没有给出自建 GitHub App 所需的具体最小权限清单，也没有说明本项目直接 HTTP 推理方式的全部授权要求。本次实测证明了指定调用可用，尚未验证所有模型、账户类型或组织策略下的兼容性。

以下项目未验证：

- 自建 GitHub App 后台各权限对模型访问的影响。
- 同账号通过现有 OpenCode OAuth App 登录时的模型目录与推理结果。
- 自建 OAuth App 对目录内模型的推理能力：本次只向 `gpt-6-astra` 发起请求，未重复测试 `gpt-4o-mini`。
- 当前目录外其他模型的实际可用性，以及 `gpt-6-astra` 被拒绝的具体原因。
- 新 OAuth token 的长期有效性和可选过期配置。
- Copilot SDK 接入后的模型目录、调用结果与计费情况。
- 实际额度扣减、组织费用归属和刷新并发控制。

## 测试材料与清理

临时测试程序位于 `scripts/check-copilot-app/`，包含 `main.go` 与 `main_test.go`。删除前，4 项模拟流程测试和 `go vet` 已通过，覆盖设备登录、凭据脱敏、失败诊断、路由保护及目录外新模型的 Responses 请求。

归档时已删除上述临时 Go 代码，保留本文记录的脱敏结果。后续 OAuth App 测试使用的 PowerShell 脚本位于仓库外的 `copilot-oauth-probe-script-*` 临时目录；本次文档更新没有新增仓库内测试代码，正式客户端未修改。

凭据和诊断报告保存在仓库外的系统临时目录 `copilot-app-probe-*`、`copilot-oauth-probe-*` 中。凭据 JSON 为明文文件，未纳入版本控制；本次没有删除这些临时凭据文件。

## 参考

- [OpenCode Copilot provider 源码](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/provider/github-copilot.ts)
- [创建 OAuth App](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/creating-an-oauth-app)
- [OAuth App 授权与 Device Flow](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/authorizing-oauth-apps)
- [OAuth scopes](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/scopes-for-oauth-apps)
- [选择 GitHub App 权限](https://docs.github.com/en/apps/creating-github-apps/registering-a-github-app/choosing-permissions-for-a-github-app)
- [GitHub App 与 OAuth App 的区别](https://docs.github.com/en/apps/oauth-apps/building-oauth-apps/differences-between-github-apps-and-oauth-apps)
- [刷新 GitHub App 用户 token](https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app/refreshing-user-access-tokens)
- [Token 过期与撤销](https://docs.github.com/en/authentication/keeping-your-account-and-data-secure/token-expiration-and-revocation)
- [Copilot SDK 用户 OAuth 接入](https://docs.github.com/en/copilot/how-tos/copilot-sdk/setup/github-oauth)
- [Copilot SDK 组织计费的服务端接入](https://docs.github.com/en/copilot/how-tos/copilot-sdk/auth/server-to-server-tokens)
