# GitHub Copilot integration

The `internal/copilot` package implements device authorization and account model discovery with Go's standard library. Its protocol follows OpenCode v2 source verified on **2026-10-15**. The desktop layer owns secure token storage and tool execution.

## Authorization

1. `NewClient("").BeginLogin(ctx)` posts JSON to `https://github.com/login/device/code`:

   ```json
   {"client_id":"Ov23li8tweQw6odWQebz","scope":"read:user"}
   ```

2. Display `DeviceCode.UserCode`, and open `DeviceCode.VerificationURI` in the system browser. This client ID is the public OAuth application used by OpenCode; it is not a secret. A separately distributed product should assess whether to register its own OAuth application with the required Copilot access.
3. `PollLogin(ctx, code)` posts `client_id`, `device_code`, and `grant_type: "urn:ietf:params:oauth:grant-type:device_code"` to `https://github.com/login/oauth/access_token`. `authorization_pending` waits the advertised interval plus OpenCode's three-second safety margin. `slow_down` uses the new interval or adds five seconds. The operation honors cancellation and code expiry and reports explicit denial.
4. Once GitHub returns `access_token`, the client requests `https://api.github.com/copilot_internal/user` with `Authorization: Bearer <access_token>` and `X-GitHub-Api-Version: 2025-04-01`. An explicit `chat_enabled: false` produces an entitlement error. A temporary or malformed entitlement lookup does not deny login. A permitted HTTPS `endpoints.api` overrides the default `https://api.githubcopilot.com` endpoint.
5. The caller owns secure storage of the returned token and can reconstruct the client with `NewClient(token)` on startup. Successful polling also configures the existing client.

Current OpenCode v2 sends the GitHub OAuth token directly to the Copilot service. It does **not** exchange it through the older `/copilot_internal/v2/token` endpoint. There is no refresh-token operation in this flow; authentication failures require reconnecting the account.

`NewClientForHost(token, host)` also supports GitHub Enterprise deployments ending in `.ghe.com`. The host argument is a hostname such as `acme.ghe.com`; URLs, paths, ports, credentials, invalid DNS labels, and unrelated domains are rejected. `NewClient(token)` remains the public GitHub.com constructor. Each client owns its token, endpoint, and model cache independently. `Host()` returns the normalized deployment; `Account(ctx)` calls that deployment's `/user` API using the GitHub REST header `X-GitHub-Api-Version: 2022-11-28` and returns `AccountInfo{Login, Host}`. This REST version is independent of the Copilot entitlement and inference versions.

| Operation | GitHub.com | Enterprise `acme.ghe.com` |
| --- | --- | --- |
| Device OAuth | `https://github.com/login/...` | `https://acme.ghe.com/login/...` |
| Account and entitlement | `https://api.github.com` | `https://api.acme.ghe.com` |
| Copilot fallback | `https://api.githubcopilot.com` | `https://copilot-api.acme.ghe.com` |

The returned device verification URL must use HTTPS on the selected deployment. A regional Copilot endpoint must use HTTPS with no credentials, explicit port, query, fragment, or non-root path. Public clients accept only `githubcopilot.com` and its subdomains; enterprise clients accept only subdomains of their configured enterprise domain. Cross-enterprise and public-to-enterprise endpoint overrides are ignored. This is deliberately stricter than upstream's trust in the entitlement response; deployments that report an unrelated regional domain would require a reviewed allowlist change. Reauthorizing clears the old model cache and resets the endpoint to the same deployment's fallback before entitlement discovery.

## Models and requests

`Models(ctx)` gets `{accountEndpoint}/models` with the OAuth bearer token and `X-GitHub-Api-Version: 2026-08-01`. It excludes disabled models and the incompatible `gpt-5-chat-latest` alias. Models with `model_picker_enabled: false` remain cached for utility calls but are omitted from the visible list. Live model metadata determines name, image/PDF/tool/reasoning support, output limit, and endpoint. `VisionKnown`, `PDFKnown`, `ToolsKnown`, and `ReasoningKnown` distinguish absent metadata from an explicit negative. A model name never supplies a capability flag.

Advertised endpoints take precedence in this order:

| Endpoint | Request format | Text stream |
| --- | --- | --- |
| `/v1/messages` | Anthropic `messages`, top-level `system`, `max_tokens`, `tools[].input_schema` | `content_block_delta.delta.text` |
| `/responses` | OpenAI Responses `input`, `store: false`, `include: ["reasoning.encrypted_content"]`, function tools | `response.output_text.delta` |
| `/chat/completions` | OpenAI-compatible `messages` and `tools[].function` | `choices[0].delta.content` |

If endpoint metadata is absent, GPT generation 5 and later (except `gpt-5-mini`), Grok, and MAI Code use Responses; other models use Chat Completions. This fallback matches the inspected OpenCode source.

Every inference request includes `Content-Type: application/json`, `Accept: text/event-stream`, `Authorization: Bearer <token>`, an application User-Agent, `Openai-Intent: conversation-edits`, `X-GitHub-Api-Version: 2026-08-01`, and `X-Interaction-Type: conversation-agent`. `ConversationID` supplies `X-Interaction-Id`, `X-Session-Id`, and `x-session-affinity`; callers retain the same ID for follow-ups and tool continuations. `x-initiator` is `user` for a user turn and `agent` for a tool continuation. Background requests, including native chat title generation, set `Request.Background` and use `conversation-background` with `x-initiator: agent`. Image input adds `Copilot-Vision-Request: true`. Anthropic requests also include `anthropic-version: 2023-06-01` and `anthropic-beta: interleaved-thinking-2025-05-14`.

Image attachments are supplied as base64 bytes and serialized in each endpoint's image format. UTF-8 text attachments are appended to the message with their filenames. PDF attachments are accepted only when model metadata advertises PDF support and the endpoint supports them. Unsupported binary files produce a useful error rather than being silently dropped.

### Prompt caching

Cache hits are reported by the service and depend on an unchanged, sufficiently long prompt prefix. The client now supplies the cache controls supported by each route:

| Route | Cache request behavior |
| --- | --- |
| Responses | A stable `prompt_cache_key` is the 64-character SHA-256 digest of `ConversationID`. No key is invented when the ID is absent. Requests continue to send full history with `store: false`; caching does not use `previous_response_id`. |
| Chat Completions | Stable session headers supply routing affinity. No `prompt_cache_key` body field or Anthropic `cache_control` is sent because Copilot does not advertise their support on this route. |
| Anthropic Messages | Up to four `cache_control: {type: "ephemeral"}` breakpoints mark the last tool definition, first and last distinct system blocks, and the latest cacheable conversation block. The conversation boundary advances after tool results and follow-ups. Thinking/signature/redacted blocks are never marked or rewritten. |

Tool definitions are sorted by name on a copy so an MCP server's enumeration order cannot change the prompt prefix. System instructions stay in ordered Anthropic text blocks; empty turns are omitted, consecutive Anthropic tool results share one user turn, and actual message text, attachments, tool IDs, and opaque reasoning remain unchanged. Cache markers are added to newly constructed request objects, never saved history.

A short prompt, first cache write, expired/evicted cache, changed model/tools/system prefix, or backend routing can all produce a legitimate zero. OpenCode's recorded OpenAI cache test uses a prefix beyond the 1024-token threshold; Copilot and Anthropic model-specific thresholds remain service-controlled. No padding or fabricated cached-token count is added to make the UI show a hit.

### Thinking controls

`Model.ThinkingEfforts` contains the exact unique, nonempty values advertised by `capabilities.supports.reasoning_effort`; there is no hardcoded low/medium/high menu. For Anthropic's Messages route, named effort is exposed only when `adaptive_thinking` is also advertised. Budget-based models instead expose `MinThinkingBudget` and `MaxThinkingBudget` without invented effort labels. Omitting both request fields leaves the provider's default behavior in place.

| Route | `Request.ReasoningEffort` | `Request.ThinkingBudgetTokens` |
| --- | --- | --- |
| Responses | `reasoning: {effort, summary: "auto"}` | Unsupported |
| Chat Completions | `reasoning_effort: effort` | `thinking_budget: tokens` |
| Anthropic Messages, adaptive | `thinking: {type: "adaptive", display: "summarized"}`, `output_config: {effort}` | Unsupported |
| Anthropic Messages, budget | Unsupported | `thinking: {type: "enabled", budget_tokens: tokens}` |

The client rejects an unadvertised effort, an out-of-range budget, or simultaneous effort and budget before sending inference. Anthropic budgets also require at least 1024 tokens, must be smaller than the model's output limit, and raise `max_tokens` when necessary so the thinking budget fits. Tool requests are rejected when live metadata explicitly says tool calls are unsupported.

## Streaming and MCP

Use `Stream(ctx, request, onDelta)` for text-only interaction. `StreamWithTools` returns a `Completion` containing the text, tool calls, provider reasoning metadata, usage, and timings. Both legacy callbacks receive answer text only. `StreamWithEvents` additionally reports `StreamEvent{Kind: EventReasoning, Delta: ...}` for provider-visible reasoning, and `EventText` for answer text. Callbacks run synchronously on the calling goroutine; UI code must marshal updates onto its UI thread.

Visible reasoning comes only from Chat `reasoning_text`, Responses `reasoning_summary_text.delta`, and Anthropic `thinking` text, or their ordinary JSON equivalents. The client does not decode encrypted reasoning or emit signatures, opaque state, or redacted blocks. A provider may return no visible reasoning even when thinking is enabled.

```go
result, err := client.StreamWithTools(ctx, request, appendVisibleText)
if err != nil {
    // Keep result.Content if a network failure or cancellation left partial text.
    return err
}
request.Messages = append(request.Messages, result.AssistantMessage())
for _, call := range result.ToolCalls {
    // Apply application policy and dispatch to the configured MCP server.
    output := executeTool(call.Name, call.Arguments)
    request.Messages = append(request.Messages, copilot.Message{
        Role: "tool", ToolCallID: call.ID, Content: output,
    })
}
// If tools were returned, request the next model turn with the expanded history.
```

`Tool.InputSchema` is a JSON-schema object. `ToolCall.Arguments` is a complete JSON object string, validated after streaming. Tool execution and iteration limits belong to the application. `Completion.AssistantMessage()` retains the opaque reasoning state needed by later turns, including Copilot Chat reasoning signatures and encrypted Responses reasoning items.

SSE parsing handles comments, CRLF, multiline data, UTF-8, large chunks, and final events without a trailing blank line. API errors, invalid JSON, incomplete streams, output limits, and cancellation return errors while preserving accumulated text. HTTP redirects are disabled so credentials cannot be forwarded to redirect destinations. No request bodies or credentials are logged.

### Usage and timing

`Completion.Usage` contains provider-reported counts only. `Available` means at least one usable count was received. Check `InputAvailable`, `OutputAvailable`, and `CachedInputAvailable` individually before presenting their corresponding values; an absent count is different from a reported zero. No tokenizer estimate replaces missing data.

| Route | Input tokens | Output tokens | Cached input tokens |
| --- | --- | --- | --- |
| Chat Completions | `prompt_tokens` | `completion_tokens` | `prompt_tokens_details.cached_tokens` |
| Responses | `input_tokens` | `output_tokens` | `input_tokens_details.cached_tokens` |
| Anthropic Messages | `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` | `output_tokens` | `cache_read_input_tokens` |

Anthropic input counts arrive in `message_start`; later usage events contain cumulative output counts, which replace rather than add to previous values. Cache creation counts are included in total input but are not cache hits. Usage already received survives incomplete or failed streams. Chat counts depend on what the Copilot service returns; a missing usage event remains unavailable.

`Completion.Metrics.Duration` measures the inference HTTP request through stream completion or failure, including callback time. Model discovery and request preparation are outside that interval. `TimeToFirstToken` measures the first nonempty visible answer or reasoning fragment from the same start. A tool-only response or response with no visible fragment leaves it at zero. Both fields are Go `time.Duration` values; their JSON representation is nanoseconds. Timing is local elapsed time, not provider-side model execution time.

## Chat titles

OpenCode uses `gpt-4o-mini` when the live account model list contains it, with a non-streaming `/chat/completions` request, a small `max_tokens`, `x-initiator: agent`, and `X-Interaction-Type: agent-session-name-generation`. Its source describes this utility route as free and rate limited. Availability and billing policy remain controlled by GitHub. The package exposes ordinary model inference; it does not automatically issue background title calls.

## Validation and limitations

`go test ./internal/copilot` uses mock HTTP servers and transports to cover public/enterprise domain and credential isolation, device polling and cancellation, invalid verification URLs, entitlement rejection, live capability flags, exact effort lists, thinking request validation for all three routes, streamed tool calls, visible versus opaque reasoning, usage and missing-count semantics, non-streaming responses, local timings, attachments, SSE framing, and failure/partial-result handling. Cache tests verify stable routing across follow-ups and tool continuations, unchanged message/tool prefixes, the four-breakpoint limit, signed-history preservation, and distinct cache-write/cache-read accounting. These tests verify the request contract and parsing; their simulated cache-hit response is not evidence of a real Copilot cache hit. They do not require credentials or consume account quota. Live end-to-end authorization and inference require an entitled GitHub account and are not claimed by the test suite; enterprise behavior has been checked against protocol mocks rather than a real enterprise tenant.

Limited live verification on public GitHub Copilot has confirmed a Responses-route cache read with synthetic context. That result does not guarantee cache hits for other models or histories; regular tests remain offline, and Anthropic/Chat cache behavior remains subject to service policy.

Copilot endpoints and client entitlement are GitHub-controlled and can change. Refreshing the live model list is preferable to hardcoding model IDs. This implementation has no automatic request retries, so a failed tool-producing request cannot silently execute twice. OAuth tokens should be protected using the operating system's credential storage or equivalent local encryption.

## Verified upstream references

- [OpenCode v2 Copilot provider documentation](https://opencode.ai/v2/docs/providers/#copilot)
- [Device OAuth, entitlement, inference headers, and utility title implementation](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/plugin/provider/github-copilot.ts)
- [Live model catalog schema and endpoint selection](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/github-copilot/models.ts)
- [Chat request and stream implementation](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/github-copilot/chat/openai-compatible-chat-language-model.ts)
- [Chat messages and reasoning replay](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/github-copilot/chat/convert-to-openai-compatible-chat-messages.ts)
- [Responses request and stream implementation](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/github-copilot/responses/openai-responses-language-model.ts)
- [Responses input conversion](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/github-copilot/responses/convert-to-openai-responses-input.ts)
- [Stable session headers and prompt cache identity](https://github.com/anomalyco/opencode/blob/dev/packages/core/src/session/model-request.ts)
- [Anthropic automatic cache breakpoint policy](https://github.com/anomalyco/opencode/blob/dev/packages/ai/src/cache-policy.ts)
- [Chat cache-key compatibility gating](https://github.com/anomalyco/opencode/blob/dev/packages/ai/src/protocols/openai-chat.ts)
- [Recorded OpenAI cache test and prefix threshold](https://github.com/anomalyco/opencode/blob/dev/packages/ai/test/provider/openai-responses-cache.recorded.test.ts)
