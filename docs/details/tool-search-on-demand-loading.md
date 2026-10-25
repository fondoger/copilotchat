# 工具按需搜索：从全量加载到客户端发现

前三部分解释工具搜索机制及 Claude Code／Codex 的实际客户端行为；第四部分记录本网关当前实现、实测范围与兼容边界。

阅读时始终区分三个问题：**谁搜索工具目录、完整 schema 何时进入模型上下文、谁执行实际函数。** 它们不是同一件事。

## 一、基础：全量加载、本地搜索与服务端搜索

### 1. 首轮全量加载

假设客户端有 100 个工具，其中一个是 `read_file`。全量加载时：

```text
客户端把 100 个工具的完整定义放进请求
  → API 把这 100 份 schema 提供给模型
  → 模型选择 read_file，并生成参数
  → 客户端检查权限、执行读取、回传结果
```

没有搜索步骤。即使本轮只用一个工具，模型仍提前看到全部定义。工具很多时，会增加上下文占用，也可能让工具选择更困难。

### 2. 本地 ToolSearch：客户端搜索，模型服务负责推理

“本地”指搜索由 Agent 客户端运行，不表示搜索到的工具都在本机执行。客户端可以搜索本地维护的 MCP 工具目录，实际 MCP 调用仍可能发往远程服务器。

```text
用户：查一下我的日历
  → 模型调用搜索入口，查询“日历查询工具”
  → 模型服务返回搜索调用，等待客户端处理
  → 客户端搜索自己的工具目录，找到 calendar_lookup
  → 客户端把命中工具的引用或 schema 放进下一轮请求
  → API 向模型提供命中的 schema
  → 模型调用 calendar_lookup
  → 客户端处理权限、调用对应 MCP 服务、回传结果
```

这里有两次不同的工具调用：

- 搜索调用：找到应该使用哪个工具。
- 业务调用：真正查询日历。

### 3. 服务端 ToolSearch：API 在请求内部搜索

```text
客户端提交服务端搜索声明和候选工具目录
  → 模型发起搜索
  → API 服务端执行搜索，并加载命中的 schema
  → 模型继续生成业务工具调用
  → 客户端处理权限、执行实际函数、回传结果
```

与本地搜索的关键区别：搜索不需要返回客户端执行。服务端可能在一次模型请求内完成“搜索 → 加载 → 生成业务调用”。

两家 API 都提供服务端搜索能力，但**选择搜索方式的协议标识不同**。

#### OpenAI Responses：固定 type，用 execution 区分执行位置

使用原生工具搜索协议时，客户端和服务端搜索都声明固定的 `type:"tool_search"`，不是分别定义两个不同名称的普通 function：

```json
{"type": "tool_search", "execution": "client", "parameters": {"type": "object", "properties": {"query": {"type": "string"}}, "required": ["query"]}}
```

```json
{"type": "tool_search", "execution": "server"}
```

- `execution:"client"`：模型把搜索调用交给客户端；客户端搜索后回传匹配 `call_id` 的 `tool_search_output`，命中定义可以后续再提供。
- `execution:"server"`：由 API 服务端搜索，客户端需要提前提供可搜索的 deferred 工具目录。省略 `execution` 时也默认使用 server，因此最简声明是 `{"type":"tool_search"}`。


#### Anthropic Messages：固定服务端 type，客户端则用普通自定义工具

服务端搜索由两个固定的工具 **`type` 标识**启用，不是靠描述文本猜测：

```json
{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}
```

```json
{"type": "tool_search_tool_bm25_20251119", "name": "tool_search_tool_bm25"}
```

使用这种服务端搜索时，**所有希望服务端搜索到的延迟工具，都必须把完整定义列在请求的顶层 `tools` 中，并设置 `defer_loading:true`**。搜索入口自身不能 deferred；其他希望直接使用的工具不设 deferred。服务端必须先拿到目录，才能搜索并展开定义，不能指望它搜索客户端尚未提交的工具。

客户端搜索则不使用上述两个内置类型，也没有对应的 `execution:"client"` 开关。客户端定义一个**普通自定义工具**，选择自己的 `name`，通过 `description`／prompt 说明如何搜索、搜索范围和参数，并提供实现。例如：

```json
{
  "name": "find_available_tools",
  "description": "Search the client tool catalog. Use this tool to discover tools before calling them.",
  "input_schema": {
    "type": "object",
    "properties": {"query": {"type": "string"}},
    "required": ["query"]
  }
}
```

`ToolSearch` 是这样的自定义名称之一，不是 Anthropic 保留的客户端搜索 type。模型调用后，客户端执行自己的搜索代码，再回传普通 `tool_result`。如果结果使用 `tool_reference`，同一次请求的 `tools` 必须提供被引用工具的完整定义，但**不要求首轮预先提交整个客户端目录**。

| API | 如何选择服务端搜索 | 如何选择客户端搜索 |
| --- | --- | --- |
| OpenAI Responses 原生搜索 | `type:"tool_search"` ＋ `execution:"server"`（也可省略 execution） | 同一 `type:"tool_search"` ＋ `execution:"client"` |
| Anthropic Messages | 固定 regex／BM25 内置 `type`，完整 deferred 目录提前随请求提交 | 普通自定义工具的 `name`、描述／prompt 和客户端实现；无内置 client 搜索 type |

#### OpenAI：tool_search_output 与 additional_tools 的区别

两者都能携带完整工具 schema，但协议角色不同：

| | `tool_search_output` | `additional_tools` |
| --- | --- | --- |
| 表达的意思 | “你刚才要找的工具，找到了这些。” | “不管你有没有搜索，现在再提供这些工具。” |
| 产生背景 | 模型先发起 `tool_search_call`，客户端或服务端执行搜索 | 调用方主动在会话的某个位置增加工具定义，不要求模型先搜索 |
| 与搜索调用的关联 | 是；客户端路径通过 `call_id` 对应搜索调用 | 无，不需要搜索调用或 `call_id` |
| 承载位置 | Responses 的 `input` 历史 | Responses 的 `input` 历史，使用 `role:"developer"` |

这里不是“模型自己执行了搜索”：模型提出搜索请求，真正搜索由客户端或服务端完成。`tool_search_output` 负责报告搜索结果，并让模型获得命中的定义。

`additional_tools` 可以理解为应用在当前会话中告诉 Agent：“接下来还有这些额外工具可用，完整 schema 如下。”这里的调用方通常是客户端、应用或网关，不一定是用户亲自提供；它不是普通的用户文本消息。例如，应用进入新的工作阶段时，可以直接注册该阶段需要的工具，也可以把自定义搜索得到的定义放在这里。

## 二、Claude Code ＋ Anthropic 订阅：本地搜索，后续补定义

### 1. 先说明证据范围

以下实现结论来自本地非官方源码镜像 `tanbiralam/claude-code`，快照提交为 `6f6f12b37f529488b10e53928dd5508bb93535c7`（2026-05-07）；主要请求代码对应镜像中的 2026-03-31 源码。镜像声明来自 source map，并含构建修补，**不能视为 Anthropic 官方开源版本，也不能保证代表当前发布客户端的所有行为**。

官方文档可以交叉确认“只在需要时加载工具定义”这一产品行为；具体首轮 HTTP 请求的过滤和后续 schema 构造，则是对上述快照的静态分析，没有在本教程中进行真实订阅抓包。

### 2. 有没有注入 ToolSearch？谁执行？

**有。快照注册了普通自定义工具 `ToolSearch`，由 Claude Code 客户端执行。不是声明 Anthropic 内置 regex／BM25 搜索工具。**

它支持 `select:工具名` 直接选择，也支持对工具名称、描述等元数据执行本地加权关键词搜索。成功后返回普通工具结果：

```json
{
  "type": "tool_result",
  "tool_use_id": "search_1",
  "content": [
    {"type": "tool_reference", "tool_name": "mcp__calendar__lookup"}
  ]
}
```

`tool_reference` 的展开由 Anthropic API 完成，日历函数则仍由客户端通过 MCP 调用。这条链路是**客户端搜索 ＋ 服务端引用展开**。

### 3. 首轮是否把全部定义给足，只标记 defer？

**不是。该快照明确过滤掉尚未发现的 deferred 工具。**

以两个工具为例：

- `Read`：常用、非 deferred。
- `mcp__calendar__lookup`：符合延迟加载条件。

在一段尚无发现历史的新会话中，首轮请求大致为：

```text
tools：Read、ToolSearch，以及其他非 deferred 工具
不包含：mcp__calendar__lookup 的完整 schema
其他提示内容：可以列出 deferred 工具名称，帮助模型搜索
```

工具名称列表不是完整参数 schema。源码也没有使用 `DeferredToolPlaceholder` 来代替整个未发现目录。

### 4. 搜索后的下一轮具体发送什么？

```text
第 1 次 API 请求
  tools = [Read, ToolSearch, …]
  → Claude 返回 ToolSearch 调用

Claude Code 在客户端执行搜索
  → 找到 mcp__calendar__lookup
  → 生成包含 tool_reference 的 tool_result

第 2 次 API 请求
  tools = [Read, ToolSearch, …,
           mcp__calendar__lookup 的完整定义，defer_loading:true]
  messages = [此前历史、搜索调用、含 tool_reference 的搜索结果]
  → Anthropic API 在引用位置展开日历工具定义
  → Claude 返回日历查询调用

Claude Code 调用对应 MCP 服务
  → 第 3 次请求回传业务结果，并保留发现历史及需要的定义
```

**为什么已经搜索到了，还标记 `defer_loading:true`？** 因为这里仍利用 API 的原生延迟加载：schema 虽然出现在 HTTP 请求的 `tools`，但由历史中的引用触发对话内展开，而不是把它当作首轮常驻的前置定义。

快照从历史 `tool_reference` 提取已发现名称，再决定本轮发送哪些定义。因此它是“后续 API 逐步补齐命中定义”，不是“第一轮发送全目录再由服务端搜索”。

### 5. 启用条件与不要混淆的情况

- 不是所有工具都 deferred：MCP 工具和显式声明可延迟的工具受候选规则控制，常用／强制常驻工具可以不延迟。
- 启用受模型能力、环境开关、工具过滤和运行时条件影响；订阅身份本身不等于搜索必然启用。
- 关闭搜索时，快照移除 `ToolSearch`，不再用这条过滤链路隐藏 deferred 定义，回到提前提供工具定义的方式。
- `auto` 阈值模式与显式启用模式不同；不能把“支持工具搜索”理解为任何配置下首轮都只发搜索入口。
- Anthropic API 的内置服务端搜索示例可以首轮提交完整 deferred 目录，那是第一部分介绍的**另一条链路**，不是此处快照的客户端实现。

### 6. 源码核对点

以下链接固定到所分析的镜像快照，而不是随时变化的 `main`：

- [注册 ToolSearch](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/tools.ts#L247-L249)。
- [过滤尚未发现的 deferred 工具](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/services/api/claude.ts#L1154-L1171)：注释明确说明不再预声明整个目录。
- [仅序列化过滤后的工具，并设置 deferLoading](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/services/api/claude.ts#L1231-L1245)。
- [完整定义字段及 defer_loading 序列化](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/utils/api.ts#L211-L225)。
- [客户端搜索处理和引用结果](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/tools/ToolSearchTool/ToolSearchTool.ts#L328-L469)。

## 三、Codex ＋ OpenAI／ChatGPT 订阅：本地搜索，schema 放进搜索历史

### 1. 证据范围与适用路径

以下结论来自官方 `openai/codex` 源码快照 `970111f6cb62cc02fe36a82ea7d71b37a2889c97`（2026-10-23），描述**普通 Responses、原生客户端 tool search** 路径。Code Mode 的嵌套搜索和 Responses Lite 的声明承载方式不同，不把它们混入本例。

订阅登录在源码中走 ChatGPT auth，默认后端为 `https://chatgpt.com/backend-api/codex`（配置可覆盖）。这不代表搜索在 ChatGPT 服务端执行：执行位置仍由下面的声明和 handler 决定。TypeScript SDK 包装 CLI，没有另外实现一套搜索引擎。

### 2. 有没有搜索工具？是不是服务端工具？

**有，但不是普通自定义 function 名称，而是 Responses 原生搜索类型，明确指定客户端执行：**

```json
{
  "type": "tool_search",
  "execution": "client",
  "description": "Search deferred tools",
  "parameters": {
    "type": "object",
    "properties": {
      "query": {"type": "string"},
      "limit": {"type": "integer"}
    },
    "required": ["query"]
  }
}
```

模型返回 `tool_search_call` 后，Codex Rust runtime 执行搜索。该快照使用本地 BM25，对 deferred registry 中的工具元数据检索，再把命中结果返回模型服务。

**原生 `tool_search` 类型不等于 server 执行；`execution:"client"` 才是这里的关键。** 不应把这条链路称为“Codex 服务端 tool search”。

### 3. 首轮和后续请求分别包含什么？

首轮发送直接可见的工具及客户端搜索入口，**不发送尚未加载的 deferred schema**。搜索后，命中 schema 放到下一次请求的 `input` 历史中：

```json
{
  "type": "tool_search_output",
  "execution": "client",
  "call_id": "search_1",
  "status": "completed",
  "tools": [{
    "type": "namespace",
    "name": "calendar",
    "tools": [{
      "type": "function",
      "name": "lookup",
      "description": "Look up calendar events",
      "defer_loading": true,
      "parameters": {
        "type": "object",
        "properties": {"date": {"type": "string"}},
        "required": ["date"]
      }
    }]
  }]
}
```

这个结果同时承载“搜索找到了什么”和“找到的工具如何调用”。API 根据该原生搜索结果向模型提供定义，模型随后生成对应函数调用。

```text
第 1 次 API 请求
  tools = [直接可见工具, {type:tool_search, execution:client}]
  → 模型返回 tool_search_call(query="calendar")

Codex 本地执行 BM25 搜索
  → 将命中工具整理成可加载的 namespace／function 定义

第 2 次 API 请求
  tools = 仍然只有直接可见工具和搜索入口
  input = [此前历史、搜索调用、携带 schema 的 tool_search_output]
  → 模型调用 calendar.lookup

Codex 处理权限并执行业务调用
  → 第 3 次请求回传函数结果
  → schema 继续随 tool_search_output 历史保留，不追加到顶层 tools
```

**这与前面的 Claude Code 快照不同：Codex 不把搜索到的定义再注入后续顶层 `tools`。** 源码集成测试专门验证第一次请求、搜索后的请求和业务调用后的请求都不会出现这种顶层注入。

命中定义会规范化：函数输入参数被保留并标记 `defer_loading:true`，但不保证保留原始工具声明中的所有扩展字段，例如这条路径会移除 `output_schema`。

### 4. 哪些工具会按需搜索？

不是所有 Codex 工具都延迟加载：

- 模型须声明 `supports_search_tool`，暴露策略允许搜索，且确实存在可搜索的 deferred 工具。
- MCP 工具、Apps connector 和宿主提供的 dynamic tools 受各自暴露规则影响；常规内置工具仍可直接提供。
- Apps 功能还受启用状态和 ChatGPT auth 条件影响；普通 MCP 搜索不能一概说成订阅专属能力。
- 老的 `tool_search` feature flags 在该快照中已经是移除／无操作项，不能用这些旧开关判断当前请求行为。

### 5. 源码核对点

- [搜索声明明确为 execution:client](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/handlers/tool_search_spec.rs#L93-L105)。
- [本地 BM25 索引和评分](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/handlers/tool_search.rs#L167-L175)。
- [客户端搜索结果转成 tool_search_output](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/context.rs#L284-L298)。
- [模型可见工具表排除非直接可见项](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/spec_plan.rs#L628-L659)。
- [集成测试：后续请求依赖搜索历史，不注入顶层工具](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/tests/suite/search_tool.rs#L741-L831)。

### 6. 最后用一张表对齐概念

| 问题 | Claude Code 快照：Anthropic 订阅 | Codex 快照：ChatGPT 订阅 |
| --- | --- | --- |
| 有无搜索入口 | 普通客户端工具 `ToolSearch` | 原生 `tool_search`，`execution:client` |
| 谁搜索 | Claude Code 客户端 | Codex 客户端，本地 BM25 |
| 首轮是否发送未发现的完整 deferred 目录 | 否 | 否 |
| 命中的 schema 下一轮放哪里 | 顶层 `tools`，仍标记 deferred | 历史 `tool_search_output.tools` |
| 谁让模型看到 schema | Anthropic API 展开历史 `tool_reference` | Responses API 处理原生搜索结果 |
| 谁执行业务函数 | 客户端或其连接的 MCP 服务 | 客户端或其连接的 MCP 服务 |
| 是否因此可称为服务端搜索 | 否 | 否 |

工具按需搜索不只是“少发几个 JSON 字段”，更不是“API 已经接管工具执行”。理解它时应同时看**首轮请求、搜索调用由谁处理、下一轮 schema 的承载位置**，不能只看模型品牌、订阅类型或工具名称。

官方资料：[Claude Code MCP 工具搜索](https://code.claude.com/docs/en/mcp#scale-with-mcp-tool-search) · [Anthropic 内置与自定义工具搜索](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool) · [OpenAI 服务端与客户端工具搜索](https://developers.openai.com/api/docs/guides/tools-tool-search)

## 四、本网关当前实现

### 路由规则

使用 Responses 上游且 **Provider 原始模型 ID 含 `gpt`（不区分大小写）**时，使用 OpenAI 原生搜索协议，不看公开路由别名或 UI 名称：

| 输入 | GPT Responses 上游 | 客户端看到的返回 |
| --- | --- | --- |
| Claude Code SDK `ToolSearch`（必填 `query:string`，可选 `max_results`） | `tool_search`，`execution:client`；结果引用解析为 `tool_search_output.tools` | 普通 `tool_use`，名称恢复为 `ToolSearch`，参数保持对象 |
| Anthropic regex／BM25 内置搜索类型 | `tool_search`，`execution:server`；提交完整 deferred 工具目录 | `server_tool_use` ＋ `tool_search_tool_result`，业务调用仍交客户端执行 |
| Codex 原生 Responses 搜索 | 保留原生声明、搜索结果、namespace 和历史，不降成普通 function | 原生 Responses 输出 |

客户端自行提供的该原生 item 仍正常透传；它用于主动注册工具，不是客户端搜索结果的替代品。

生成的 Codex 模型目录对非 GPT 的 Responses 上游设置 `supports_search_tool:false`，暂不让 Codex 主动启用搜索，以避免兼容转换改变工具表带来的缓存前缀风险；GPT Responses 和 Messages-only 模型仍按普通工具能力声明搜索支持。这是保守策略，不代表已测得具体缓存收益。

### 客户端搜索

首轮识别 Claude Code 固定工具契约，转换搜索入口并保留原始参数 schema，不把 `max_results` 偷换成 `limit`。不要求首轮提交未发现的完整目录。

历史通过 `tool_use.id` 与 `tool_result.tool_use_id` 配对；每次命中都从当前工具表查定义，生成 `tool_search_output.tools`，不再向 GPT 顶层工具表追加已发现定义。零结果返回空 `tools`，客户端关于未命中／仍在连接的文本作为相邻用户消息保留。

不符合 SDK 契约的同名普通函数不转换；多个搜索入口、未知引用、未完成／重复调用、错误结果或无法表达的混合搜索结果明确拒绝。普通业务工具结果仍按原有规则转换。

### 服务端搜索

匹配的是工具 `type`：`tool_search_tool_regex_20251119` 或 `tool_search_tool_bm25_20251119`，要求对应规范名称。两者映射为 OpenAI 服务端发现能力，**不承诺 OpenAI 执行相同的 regex／BM25 算法**。不支持的选项、多搜索声明或 client/server 混合配置明确拒绝。

OpenAI hosted 搜索的 `call_id:null` 不套用客户端调用配对。网关为 Claude 的服务端块生成确定性 ID，并用自有 opaque 回放封装保留原 OpenAI 搜索项；客户端必须随历史保留该 thinking signature 及相邻服务端块，网关校验可见内容后恢复。封装不是加密或 Anthropic 签名，不能移除、编辑或把内部回放项直接注入请求。

目前只转换普通 function 搜索结果，不支持该桥接中的 namespace 工具目录结果或并发未配对搜索。上游附加在业务 `function_call` 上的 namespace 则通过相同的回放机制保留，避免 Claude 工具调用格式没有 namespace 字段而导致续接失败。客户端只执行业务函数，不能为 `server_tool_use` 回传普通 `tool_result`。

### Grok 4.7等不支持原生Tool搜索能力的客户端

> Note: 支持原生搜索即支持插入Tools后保持缓存命中率不变。

非 GPT 的 Responses 上游仍保留客户端搜索的普通 function 兼容方案，用于已有搜索历史或其他客户端主动发来的搜索请求；发现的 schema 加入顶层工具表，未发现的 deferred 工具仍不发送，不是默认全量加载。非 GPT 跨协议 hosted 搜索明确拒绝。原生 Messages 请求保持透传。

GitHub Copilot 的 Grok 4.7 上游使用 Responses API，但两个客户端采用不同策略：

- **Codex**：本网关生成的模型目录设置 `supports_search_tool:false`，不主动启用原生客户端搜索；普通 MCP 工具直接提供完整定义。
- **Claude Code／Desktop**：当前项目默认开启客户端工具搜索，不读取 Codex 的能力字段。客户端发来的 `ToolSearch` 保留为普通 function；Claude 在本地搜索并补齐定义后，网关将发现的 schema 加入后续 Responses 顶层 `tools`。这不代表 Grok 原生支持 `tool_search`。

Claude 首轮可能不发送未发现工具的完整定义，网关不能在收到请求后直接改成全量加载。发现新工具时顶层工具表变化可能影响缓存前缀，但不代表之后每轮都无法命中；目前未验证具体缓存收益，不以命中率单独判断 token 成本。

### 实测与边界

2026-10-26 直接通过现有认证客户端测试 GitHub Copilot，使用随机无副作用工具定义，不访问真实文件，不执行真实函数：

| 模型 | 原生客户端搜索 | 原生服务端搜索 |
| --- | --- | --- |
| GPT-5.6 Sol Fast | JSON/SSE 均通过发现、schema 常量验证及结果续接 | JSON/SSE 均通过发现、schema 常量验证及结果续接 |
| GPT-6.1 Sol | JSON/SSE 均通过发现、schema 常量验证及结果续接 | JSON/SSE 均通过发现、schema 常量验证及结果续接 |

另外用新编译的临时网关实例（随机 loopback 端口，不替换现有运行实例）测试 GPT-6.1 Sol 的 Claude Messages 桥接：`ToolSearch` 客户端搜索、regex 声明及 BM25 声明的服务端映射，均通过业务参数验证和模拟结果后的历史续接。真实桥接测试使用 JSON；SSE 原生上游已实测，SSE 桥接及跨流式组合由离线测试覆盖，尚未用真实 Claude Code／Desktop UI 完整验证。

成功依据是模型确实生成对应搜索调用，随后正确调用仅在新 schema 中出现的随机函数／必填值，并在模拟函数结果后完成续接，不以 HTTP 200 单独判断支持。模型 ID 匹配规则覆盖其他 GPT，但不意味着它们已逐一实测；不据此承诺缓存命中率。

先前 GitHub Copilot Grok 4.7：普通函数基线成功；`additional_tools` 返回 422；原生 `tool_search` + `defer_loading` 组合返回 403。403 不足以定位哪项或何种策略被拒绝，也不能推广成 Grok 本身不支持原生搜索。

跨协议桥接要求完整历史，保留 reasoning／hosted 搜索回放状态。普通 Claude → Responses 使用 implicit 缓存，Auto 安全 classifier 才转换 explicit 断点；见[缓存转换](explict-cache-convert.md)。
