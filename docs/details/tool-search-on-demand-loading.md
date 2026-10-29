# 工具按需搜索与延迟加载

工具按需加载涉及三个环节：搜索工具目录、向模型提供完整 schema、执行业务函数。搜索可以由客户端或 API 服务端完成；业务函数仍由客户端或其连接的 MCP 服务执行。

本文先说明协议，再对照 Claude Code、Codex 的源码快照，最后记录本网关的转换规则与实测范围。

## 一、协议与请求流程

### 全量加载与按需加载

全量加载会在首轮请求中提供所有工具的完整定义。模型从中选择工具、生成参数，客户端检查权限并执行。工具数量增加时，完整目录会占用更多上下文。

按需加载先提供搜索入口，待模型提出搜索请求后，再加载命中工具的定义：

```text
用户要求查询日历
  → 模型发起工具搜索
  → 客户端或 API 服务端搜索目录
  → API 向模型提供 calendar_lookup 的 schema
  → 模型调用 calendar_lookup
  → 客户端检查权限、调用对应 MCP 服务、回传结果
```

客户端搜索需要将搜索调用返回客户端处理，再发起下一次 API 请求。服务端搜索可以在一次请求内完成搜索、加载定义和生成业务调用。

“客户端搜索”描述的是目录搜索的位置。目录中的工具可以连接远程 MCP 服务。

### OpenAI Responses

原生搜索统一使用 `type:"tool_search"`，由 `execution` 指定执行位置。

客户端搜索声明：

```json
{
  "type": "tool_search",
  "execution": "client",
  "parameters": {
    "type": "object",
    "properties": {"query": {"type": "string"}},
    "required": ["query"]
  }
}
```

服务端搜索声明：

```json
{"type": "tool_search", "execution": "server"}
```

省略 `execution` 时默认为 `server`。服务端搜索需要客户端提前提交可搜索的 deferred 工具目录；客户端搜索可以在命中后补交定义。

Responses 的历史中有两种提供工具定义的方式：

| 历史项 | 用途 | 与搜索调用的关系 |
| --- | --- | --- |
| `tool_search_output` | 返回搜索结果，在 `tools` 中携带命中定义 | 客户端搜索通过 `call_id` 关联 `tool_search_call` |
| `additional_tools` | 由调用方主动增加工具定义，使用 `role:"developer"` | 无需先有搜索调用 |

调用方可以是客户端、应用或网关。例如，应用进入新的工作阶段时，可以通过 `additional_tools` 注册该阶段需要的工具。

### Anthropic Messages

服务端搜索使用两个内置类型：

```json
{"type": "tool_search_tool_regex_20251119", "name": "tool_search_tool_regex"}
```

```json
{"type": "tool_search_tool_bm25_20251119", "name": "tool_search_tool_bm25"}
```

可搜索工具的完整定义须放在请求顶层 `tools` 中，并设置 `defer_loading:true`。搜索入口和需要直接使用的工具保持常驻。

客户端搜索使用普通自定义工具，由客户端实现搜索逻辑。例如：

```json
{
  "name": "find_available_tools",
  "description": "Search the client tool catalog to discover tools before calling them.",
  "input_schema": {
    "type": "object",
    "properties": {"query": {"type": "string"}},
    "required": ["query"]
  }
}
```

`ToolSearch` 是客户端选用的自定义名称。模型调用后，客户端执行搜索并返回普通 `tool_result`。若结果包含 `tool_reference`，同一次后续请求的顶层 `tools` 须提供被引用工具的完整定义；完整目录可以留在客户端，按搜索结果逐步提交。

| API | 服务端搜索 | 客户端搜索 |
| --- | --- | --- |
| OpenAI Responses | `type:"tool_search"`，`execution:"server"` 或省略 | `type:"tool_search"`，`execution:"client"` |
| Anthropic Messages | regex／BM25 内置 `type` | 普通自定义工具，由名称、描述、参数和客户端实现约定行为 |

## 二、Claude Code：客户端搜索，后续补定义

### 证据范围

本节依据非官方源码镜像 `tanbiralam/claude-code`，快照提交为 `6f6f12b37f529488b10e53928dd5508bb93535c7`（2026-05-07），主要请求代码对应镜像中的 2026-03-31 源码。镜像声明来自 source map，并含构建修补，结论限于该快照。

官方文档可交叉确认按需加载这一产品行为。首轮过滤和后续 schema 构造来自静态源码分析，本文未进行真实 Anthropic 订阅抓包。

### 搜索与定义加载

快照注册普通自定义工具 `ToolSearch`，由 Claude Code 客户端执行。它支持 `select:工具名`，也支持对工具名称、描述等元数据进行本地加权关键词搜索。命中结果通过引用返回：

```json
{
  "type": "tool_result",
  "tool_use_id": "search_1",
  "content": [
    {"type": "tool_reference", "tool_name": "mcp__calendar__lookup"}
  ]
}
```

新会话首轮只发送常驻工具和搜索入口，过滤尚未发现的 deferred 工具。提示中可以列出待发现工具的名称。

```text
第 1 次请求
  tools = [Read, ToolSearch, 其他常驻工具]
  → 模型调用 ToolSearch

Claude Code 搜索本地目录
  → 返回 mcp__calendar__lookup 的 tool_reference

第 2 次请求
  tools = [常驻工具, ToolSearch,
           mcp__calendar__lookup 的完整定义，defer_loading:true]
  messages = [此前历史, 搜索调用, 含 tool_reference 的搜索结果]
  → Anthropic API 在引用位置展开定义
  → 模型调用日历工具

Claude Code 调用对应 MCP 服务
  → 下一次请求回传业务结果，保留发现历史及所需定义
```

命中定义仍标记 `defer_loading:true`：API 根据历史引用在对话内展开 schema。快照从历史 `tool_reference` 提取已发现名称，以此决定每次请求发送哪些定义。

### 启用条件

- MCP 工具和显式声明可延迟的工具受候选规则控制；常用或强制常驻工具可以直接提供。
- 是否启用取决于模型能力、环境开关、工具过滤和运行时条件，`auto` 模式还受阈值控制。
- 关闭搜索时，快照移除 `ToolSearch`，停止隐藏 deferred 定义，恢复提前提供工具定义。

### 源码依据

以下链接固定到所分析的镜像提交：

- [注册 ToolSearch](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/tools.ts#L247-L249)
- [过滤尚未发现的 deferred 工具](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/services/api/claude.ts#L1154-L1171)
- [序列化过滤后的工具并设置 deferLoading](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/services/api/claude.ts#L1231-L1245)
- [完整定义与 defer_loading 序列化](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/utils/api.ts#L211-L225)
- [客户端搜索处理与引用结果](https://github.com/tanbiralam/claude-code/blob/6f6f12b37f529488b10e53928dd5508bb93535c7/src/tools/ToolSearchTool/ToolSearchTool.ts#L328-L469)

## 三、Codex：客户端搜索，定义随搜索历史保留

### 证据范围

本节依据官方 `openai/codex` 源码快照 `970111f6cb62cc02fe36a82ea7d71b37a2889c97`（2026-10-23），描述普通 Responses 的原生客户端搜索路径。Code Mode 的嵌套搜索和 Responses Lite 的声明承载方式另有实现。

订阅登录使用 ChatGPT auth，默认后端为 `https://chatgpt.com/backend-api/codex`，可由配置覆盖。搜索逻辑位于 Codex Rust runtime；TypeScript SDK 包装 CLI。

### 搜索与定义加载

Codex 声明原生客户端搜索：

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

模型返回 `tool_search_call` 后，客户端使用本地 BM25 检索 deferred registry 中的工具元数据。命中定义放入下一次请求的 `input`：

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

首轮顶层 `tools` 只含直接可见工具和搜索入口。后续请求通过 `tool_search_output.tools` 保留命中 schema，业务执行结果继续追加到历史中。集成测试覆盖首轮、搜索后和业务调用后的请求，验证命中定义始终留在搜索历史中。

命中定义会经过规范化：保留函数输入参数，设置 `defer_loading:true`，移除 `output_schema` 等部分扩展字段。

### 启用条件

- 模型声明 `supports_search_tool`，暴露策略允许搜索，且存在可搜索的 deferred 工具。
- MCP 工具、Apps connector 和宿主提供的 dynamic tools 各有暴露规则；常规内置工具可直接提供。
- Apps 功能还受启用状态和 ChatGPT auth 条件影响。
- 该快照中的旧 `tool_search` feature flags 已移除或不再生效。

### 源码依据

- [客户端搜索声明](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/handlers/tool_search_spec.rs#L93-L105)
- [本地 BM25 索引和评分](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/handlers/tool_search.rs#L167-L175)
- [构造 tool_search_output](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/context.rs#L284-L298)
- [构造直接可见工具表](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/src/tools/spec_plan.rs#L628-L659)
- [集成测试：命中定义随搜索历史保留](https://github.com/openai/codex/blob/970111f6cb62cc02fe36a82ea7d71b37a2889c97/codex-rs/core/tests/suite/search_tool.rs#L741-L831)

### 两个客户端的差异

| 环节 | Claude Code 快照 | Codex 快照 |
| --- | --- | --- |
| 搜索入口 | 普通自定义工具 `ToolSearch` | 原生 `tool_search`，`execution:"client"` |
| 目录搜索 | 客户端加权关键词搜索 | 客户端 BM25 |
| 首轮工具定义 | 常驻工具和搜索入口 | 直接可见工具和搜索入口 |
| 命中 schema 的位置 | 后续顶层 `tools`，标记 deferred | 历史 `tool_search_output.tools` |
| API 加载方式 | 展开 `tool_reference` | 处理原生搜索结果 |
| 业务执行 | 客户端或其连接的 MCP 服务 | 客户端或其连接的 MCP 服务 |

官方资料：[Claude Code MCP 工具搜索](https://code.claude.com/docs/en/mcp#scale-with-mcp-tool-search) · [Anthropic 内置与自定义工具搜索](https://platform.claude.com/docs/en/agents-and-tools/tool-use/tool-search-tool) · [OpenAI 服务端与客户端工具搜索](https://developers.openai.com/api/docs/guides/tools-tool-search)

## 四、本网关的转换规则

### 路由选择

使用 Responses 上游且 Provider 原始模型 ID 含 `gpt`（不区分大小写）时，网关采用 OpenAI 原生搜索协议。判断使用原始模型 ID，公开路由别名和 UI 名称不参与匹配。

| 输入 | GPT Responses 上游 | 客户端收到的结果 |
| --- | --- | --- |
| Claude Code SDK `ToolSearch` | `tool_search`，`execution:"client"`；引用解析为 `tool_search_output.tools` | 普通 `tool_use`，名称为 `ToolSearch`，参数为对象 |
| Anthropic regex／BM25 内置搜索 | `tool_search`，`execution:"server"`；提交完整 deferred 目录 | `server_tool_use` 和 `tool_search_tool_result` |
| Codex 原生 Responses 搜索 | 保留原生声明、搜索结果、namespace 和历史 | 原生 Responses 输出 |

客户端提供的原生 `additional_tools` 项保持透传。

网关生成的 Codex 模型目录对非 GPT Responses 上游设置 `supports_search_tool:false`；GPT Responses 和 Messages-only 模型按普通工具能力声明搜索支持。这一保守策略用于避免兼容转换改变顶层工具表所带来的缓存前缀风险。GPT 客户端搜索的后续缓存实测见[Tool Search 流式兼容与缓存前缀](tool-search-stream-compatibility.md)。

### Claude 客户端搜索到 GPT Responses

网关按 SDK 契约识别 `ToolSearch`：输入 schema 为对象，包含必填的 `query:string`，可带 `max_results`。转换时保留原参数 schema。

历史中的 `tool_use.id` 与 `tool_result.tool_use_id` 配对。网关从当前请求的工具表查找引用定义，生成 `tool_search_output.tools`，并从 GPT 顶层工具表移除已发现定义。首轮只需提供当时可见的工具。

零结果生成空 `tools`；未命中或仍在连接的客户端说明文本作为相邻用户消息保留。不符合 SDK 契约的同名函数按普通工具处理。

下列情况会被拒绝：多个搜索入口、未知引用、未完成或重复调用、错误结果，以及无法表达的混合搜索结果。普通业务工具结果沿用原有转换规则。

### Anthropic 服务端搜索到 GPT Responses

网关匹配 regex／BM25 的内置 `type`，并要求对应规范名称。两者都映射到 OpenAI 服务端发现能力，搜索算法由 OpenAI 上游决定。不支持的选项、多搜索声明和 client/server 混合配置会被拒绝。

OpenAI hosted 搜索的 `call_id` 可为 `null`。网关为 Claude 服务端块生成确定性 ID，并将原始 OpenAI 搜索项保存在自有 opaque 回放封装中。客户端续接时须保留 thinking signature 及相邻服务端块，网关校验可见内容后恢复原项。该封装用于回放，不具备加密或 Anthropic 签名的效力；移除、编辑封装或直接注入内部回放项会导致校验失败。

当前桥接支持普通 function 搜索结果，暂不支持 namespace 工具目录结果或并发未配对搜索。业务 `function_call` 上的 namespace 通过同一回放机制保留。客户端执行业务函数；`server_tool_use` 由服务端处理，不能配普通 `tool_result`。

### 非 GPT Responses 上游

非 GPT Responses 上游保留普通 function 兼容路径，处理已有搜索历史或客户端主动发来的搜索请求。命中 schema 加入后续顶层工具表，尚未发现的 deferred 工具继续隐藏。跨协议 hosted 搜索会被拒绝，原生 Messages 请求保持透传。

GitHub Copilot 的 Grok 4.7 使用 Responses API，两个客户端采用不同策略：

- **Codex**：生成的模型目录设置 `supports_search_tool:false`，普通 MCP 工具直接提供完整定义。
- **Claude Code／Desktop**：项目默认开启客户端工具搜索。`ToolSearch` 转为普通 function，客户端搜索并补齐定义后，网关将命中 schema 加入后续顶层 `tools`。

Claude 首轮可能只提交部分工具定义，网关只能转换已经收到的目录。发现新工具时，顶层工具表变化可能影响缓存前缀；后续请求仍可能命中缓存。原生搜索的支持情况与缓存命中率须分别验证，token 成本也不能仅由命中率判断。

## 五、实测记录与验证边界

以下保留 2026-10-26 的既有实测记录。测试直接使用现有认证客户端访问 GitHub Copilot，工具定义随机生成、无副作用，不访问真实文件或执行真实函数。

| 模型 | 原生客户端搜索 | 原生服务端搜索 |
| --- | --- | --- |
| GPT-5.6 Sol Fast | JSON／SSE 均通过发现、schema 常量验证及结果续接 | JSON／SSE 均通过发现、schema 常量验证及结果续接 |
| GPT-6.1 Sol | JSON／SSE 均通过发现、schema 常量验证及结果续接 | JSON／SSE 均通过发现、schema 常量验证及结果续接 |

Claude Messages 桥接使用新编译的临时网关实例、随机 loopback 端口，保留现有运行实例。GPT-6.1 Sol 的 `ToolSearch` 客户端搜索，以及 regex、BM25 声明的服务端映射，均通过业务参数验证和模拟结果后的历史续接。

验证成功要求模型生成对应搜索调用，随后使用新 schema 中的随机函数名和必填值，并在收到模拟函数结果后完成续接。

覆盖范围如下：

- 原生上游：已实测 JSON 和 SSE。
- Messages 桥接：已实测 JSON；SSE 桥接及跨流式组合由离线测试覆盖。
- 真实 Claude Code／Desktop UI：尚未完整验证。
- 其他模型 ID 含 `gpt` 的模型：适用当前路由规则，尚未逐一实测。
- 缓存命中率与成本收益：尚未验证。

此前的 GitHub Copilot Grok 4.7 测试中，普通函数基线成功，`additional_tools` 返回 422，`tool_search` 与 `defer_loading` 组合返回 403。该 403 的具体拒绝项和策略原因尚未定位，结论限于所测上游路径与请求组合。

跨协议桥接要求完整历史，保留 reasoning 和 hosted 搜索回放状态。普通 Claude → Responses 使用 implicit 缓存，Auto 安全 classifier 才转换 explicit 断点，详见[缓存转换](explict-cache-convert.md)。
