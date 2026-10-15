package copilot

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLiveCapabilitiesPreserveUnknownAndExactEfforts(t *testing.T) {
	client := NewClient("token")
	client.accountLoaded = true
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return mockResponse(r, "application/json", `{"data":[
		 {"id":"unknown","name":"Vision reasoning tools in name"},
		 {"id":"disabled-capabilities","capabilities":{"supports":{"vision":false,"tool_calls":false,"reasoning":false},"limits":{"vision":{"supported_media_types":[]}}}},
		 {"id":"reasoner","supported_endpoints":["/responses"],"capabilities":{"supports":{"vision":true,"tool_calls":true,"reasoning_effort":["minimal","high","high","custom-value",""," bad "]},"limits":{"vision":{"supported_media_types":["image/png","application/pdf"]}}}},
		 {"id":"adaptive","supported_endpoints":["/v1/messages"],"capabilities":{"supports":{"adaptive_thinking":true,"reasoning_effort":["medium","max"]}}},
		 {"id":"budget","supported_endpoints":["/v1/messages"],"capabilities":{"supports":{"reasoning_effort":["high"],"min_thinking_budget":1024,"max_thinking_budget":16384},"limits":{"max_output_tokens":32768}}}
		]}`), nil
	})
	models, err := client.Models(context.Background())
	if err != nil || len(models) != 5 {
		t.Fatalf("catalog: %#v, %v", models, err)
	}
	unknown := models[0]
	if unknown.Vision || unknown.PDF || unknown.Tools || unknown.Reasoning || unknown.VisionKnown || unknown.PDFKnown || unknown.ToolsKnown || unknown.ReasoningKnown || len(unknown.ThinkingEfforts) != 0 {
		t.Fatalf("capabilities inferred from a model name: %#v", unknown)
	}
	disabled := models[1]
	if disabled.Vision || disabled.PDF || disabled.Tools || disabled.Reasoning || !disabled.VisionKnown || !disabled.PDFKnown || !disabled.ToolsKnown || !disabled.ReasoningKnown {
		t.Fatalf("explicit negative capabilities not preserved: %#v", disabled)
	}
	reasoner := models[2]
	if !reasoner.Vision || !reasoner.PDF || !reasoner.Tools || !reasoner.Reasoning || !reflect.DeepEqual(reasoner.ThinkingEfforts, []string{"minimal", "high", "custom-value"}) {
		t.Fatalf("live positive flags or exact efforts lost: %#v", reasoner)
	}
	adaptive := models[3]
	if !adaptive.AdaptiveThinking || !adaptive.Reasoning || !reflect.DeepEqual(adaptive.ThinkingEfforts, []string{"medium", "max"}) {
		t.Fatalf("adaptive metadata lost: %#v", adaptive)
	}
	budget := models[4]
	if !budget.Reasoning || budget.AdaptiveThinking || len(budget.ThinkingEfforts) != 0 || budget.MinThinkingBudget != 1024 || budget.MaxThinkingBudget != 16384 || budget.MaxOutputTokens != 32768 {
		t.Fatalf("budget model received invented named effort: %#v", budget)
	}
}

func TestThinkingRequestsMatchProtocolAndAdvertisedOptions(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, effort string
		budget                 int
		adaptive               bool
		want                   map[string]any
	}{
		{"responses", "/responses", "high", 0, false, map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}}},
		{"chat", "/chat/completions", "high", 0, false, map[string]any{"reasoning_effort": "high"}},
		{"adaptive", "/v1/messages", "high", 0, true, map[string]any{"thinking": map[string]any{"type": "adaptive", "display": "summarized"}, "output_config": map[string]any{"effort": "high"}}},
		{"anthropic-budget", "/v1/messages", "", 9000, false, map[string]any{"thinking": map[string]any{"type": "enabled", "budget_tokens": float64(9000)}, "max_tokens": float64(9001)}},
		{"chat-budget", "/chat/completions", "", 1024, false, map[string]any{"thinking_budget": float64(1024)}},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("token")
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Endpoint: test.endpoint, ThinkingEfforts: []string{"high"}, AdaptiveThinking: test.adaptive, MinThinkingBudget: 1024, MaxThinkingBudget: 10000, MaxOutputTokens: 12000}
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != test.endpoint {
					t.Errorf("wrong inference route: %s", r.URL.Path)
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Fatal(err)
				}
				for key, want := range test.want {
					if !reflect.DeepEqual(body[key], want) {
						t.Errorf("incorrect %s: %#v, want %#v", key, body[key], want)
					}
				}
				for _, key := range []string{"reasoning", "reasoning_effort", "thinking", "thinking_budget", "output_config"} {
					if _, expected := test.want[key]; !expected && body[key] != nil {
						t.Errorf("foreign protocol field %s leaked into %s", key, test.endpoint)
					}
				}
				return mockResponse(r, "text/event-stream", "data: [DONE]\n\n"), nil
			})
			_, err := client.StreamWithEvents(context.Background(), Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}, ReasoningEffort: test.effort, ThinkingBudgetTokens: test.budget}, nil)
			if err != nil {
				t.Fatal(err)
			}
		})
	}

	for _, test := range []struct {
		name     string
		model    Model
		effort   string
		budget   int
		wantText string
	}{
		{"unadvertised-effort", Model{Endpoint: "/responses", ThinkingEfforts: []string{"low"}}, "high", 0, "does not advertise"},
		{"effort-case", Model{Endpoint: "/chat/completions", ThinkingEfforts: []string{"high"}}, "HIGH", 0, "does not advertise"},
		{"conflict", Model{}, "high", 1024, "not both"},
		{"no-adaptive", Model{Endpoint: "/v1/messages", ThinkingEfforts: []string{"high"}}, "high", 0, "adaptive"},
		{"negative-budget", Model{Endpoint: "/v1/messages", MaxThinkingBudget: 4096}, "", -1, "does not advertise"},
		{"unadvertised-budget", Model{Endpoint: "/chat/completions"}, "", 1024, "does not advertise"},
		{"below-minimum", Model{Endpoint: "/v1/messages", MaxThinkingBudget: 4096}, "", 1000, "does not advertise"},
		{"above-maximum", Model{Endpoint: "/chat/completions", MaxThinkingBudget: 4096}, "", 5000, "does not advertise"},
		{"output-limit", Model{Endpoint: "/v1/messages", MaxThinkingBudget: 4096, MaxOutputTokens: 4096}, "", 4096, "maximum output"},
		{"adaptive-budget", Model{Endpoint: "/v1/messages", AdaptiveThinking: true, MaxThinkingBudget: 4096}, "", 2048, "adaptive"},
		{"responses-budget", Model{Endpoint: "/responses", MaxThinkingBudget: 4096}, "", 2048, "does not support"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := requestBody(Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}, ReasoningEffort: test.effort, ThinkingBudgetTokens: test.budget}, test.model)
			if err == nil || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("invalid thinking selection accepted: %v", err)
			}
		})
	}
}

func TestUsageAndVisibleReasoningAcrossProtocols(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, stream, opaque string
	}{
		{"chat", "/chat/completions", `data: {"choices":[{"delta":{"reasoning_text":"Let me check.","reasoning_opaque":"secret-chat"}}]}

data: {"choices":[{"delta":{"content":"Answer"},"finish_reason":"stop"}]}

data: {"choices":[],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":50}}}

data: [DONE]

`, "secret-chat"},
		{"responses", "/responses", `data: {"type":"response.reasoning_summary_text.delta","delta":"Let me check."}

data: {"type":"response.output_text.delta","delta":"Answer"}

data: {"type":"response.output_item.done","item":{"type":"reasoning","id":"rs_1","encrypted_content":"secret-responses","summary":[{"type":"summary_text","text":"Let me check."}]}}

data: {"type":"response.completed","response":{"usage":{"input_tokens":100,"output_tokens":20,"input_tokens_details":{"cached_tokens":50}}}}

`, "secret-responses"},
		{"anthropic", "/v1/messages", `data: {"type":"message_start","message":{"usage":{"input_tokens":40,"cache_read_input_tokens":50,"cache_creation_input_tokens":10,"output_tokens":1}}}

data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"Let me "}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"check."}}

data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"secret-anthropic"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"secret-redacted"}}

data: {"type":"content_block_stop","index":1}

data: {"type":"content_block_start","index":2,"content_block":{"type":"text","text":"Answer"}}

data: {"type":"message_delta","usage":{"output_tokens":12}}

data: {"type":"message_delta","usage":{"output_tokens":20},"delta":{"stop_reason":"end_turn"}}

data: {"type":"message_stop"}

`, "secret-anthropic"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Ensure elapsed time is observable even on coarse Windows clocks.
				time.Sleep(2 * time.Millisecond)
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, test.stream)
			}))
			defer server.Close()
			client := testClient(server)
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Endpoint: test.endpoint}
			var reasoning, text strings.Builder
			request := Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}
			result, err := client.StreamWithEvents(context.Background(), request, func(event StreamEvent) {
				switch event.Kind {
				case EventText:
					text.WriteString(event.Delta)
				case EventReasoning:
					reasoning.WriteString(event.Delta)
				default:
					t.Errorf("unknown visible event: %#v", event)
				}
			})
			if err != nil || text.String() != "Answer" || reasoning.String() != "Let me check." || result.Content != text.String() || result.Reasoning != reasoning.String() {
				t.Fatalf("incorrect visible content: %#v, %v", result, err)
			}
			wantUsage := Usage{InputTokens: 100, OutputTokens: 20, CachedInputTokens: 50, Available: true, InputAvailable: true, OutputAvailable: true, CachedInputAvailable: true}
			if result.Usage != wantUsage {
				t.Fatalf("incorrect usage (Anthropic deltas must not add output twice): %#v", result.Usage)
			}
			if result.Metrics.TimeToFirstToken <= 0 || result.Metrics.Duration < result.Metrics.TimeToFirstToken {
				t.Fatalf("invalid inference timing: %#v", result.Metrics)
			}
			encoded, _ := json.Marshal(result.AssistantMessage())
			if !strings.Contains(string(encoded), test.opaque) || strings.Contains(text.String()+reasoning.String(), "secret-") {
				t.Fatalf("opaque state lost or shown: %s", encoded)
			}
			var compatibilityText strings.Builder
			if _, err := client.StreamWithTools(context.Background(), request, func(s string) { compatibilityText.WriteString(s) }); err != nil || compatibilityText.String() != "Answer" {
				t.Fatalf("old text-only callback changed: %q, %v", compatibilityText.String(), err)
			}
		})
	}
}

func TestUsageAvailabilityAndPartialResults(t *testing.T) {
	for _, test := range []struct {
		name, endpoint, stream string
		want                   Usage
		wantError              bool
	}{
		{"absent", "/chat/completions", "data: [DONE]\n\n", Usage{}, false},
		{"empty", "/chat/completions", "data: {\"usage\":{}}\n\ndata: [DONE]\n\n", Usage{}, false},
		{"negative", "/chat/completions", "data: {\"usage\":{\"prompt_tokens\":-1}}\n\ndata: [DONE]\n\n", Usage{}, false},
		{"zero", "/chat/completions", "data: {\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0}}\n\ndata: [DONE]\n\n", Usage{Available: true, InputAvailable: true, OutputAvailable: true}, false},
		{"partial-usage", "/chat/completions", "data: {\"usage\":{\"completion_tokens\":12}}\n\ndata: [DONE]\n\n", Usage{OutputTokens: 12, Available: true, OutputAvailable: true}, false},
		{"error-keeps-usage", "/responses", "data: {\"type\":\"response.incomplete\",\"response\":{\"usage\":{\"input_tokens\":30,\"output_tokens\":5},\"incomplete_details\":{\"reason\":\"max_output_tokens\"}}}\n\n", Usage{InputTokens: 30, OutputTokens: 5, Available: true, InputAvailable: true, OutputAvailable: true}, true},
		{"anthropic-error", "/v1/messages", "data: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":30}}}\n\ndata: {\"type\":\"message_delta\",\"usage\":{\"output_tokens\":5},\"delta\":{\"stop_reason\":\"max_tokens\"}}\n\n", Usage{InputTokens: 30, OutputTokens: 5, Available: true, InputAvailable: true, OutputAvailable: true}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient("token")
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Endpoint: test.endpoint}
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return mockResponse(r, "text/event-stream", test.stream), nil
			})
			result, err := client.StreamWithEvents(context.Background(), Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}, nil)
			if (err != nil) != test.wantError || result.Usage != test.want {
				t.Fatalf("usage or failure status lost: %#v, %v", result, err)
			}
			if result.Metrics.Duration < 0 || result.Metrics.TimeToFirstToken != 0 {
				t.Fatalf("empty output must not fabricate TTFT: %#v", result.Metrics)
			}
		})
	}
}

func TestNonStreamingUsageAndVisibleReasoning(t *testing.T) {
	for _, test := range []struct{ endpoint, body string }{
		{"/chat/completions", `{"choices":[{"message":{"content":"Answer","reasoning_text":"Check","reasoning_opaque":"secret"}}],"usage":{"prompt_tokens":10,"completion_tokens":2}}`},
		{"/responses", `{"output":[{"type":"reasoning","encrypted_content":"secret","summary":[{"type":"summary_text","text":"Check"}]},{"type":"message","content":[{"type":"output_text","text":"Answer"}]}],"usage":{"input_tokens":10,"output_tokens":2}}`},
		{"/v1/messages", `{"content":[{"type":"thinking","thinking":"Check","signature":"secret"},{"type":"text","text":"Answer"}],"usage":{"input_tokens":10,"output_tokens":2}}`},
	} {
		t.Run(test.endpoint, func(t *testing.T) {
			client := NewClient("token")
			client.accountLoaded = true
			client.models["model"] = Model{ID: "model", Endpoint: test.endpoint}
			client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				return mockResponse(r, "application/json", test.body), nil
			})
			var visible strings.Builder
			result, err := client.StreamWithEvents(context.Background(), Request{Model: "model", Messages: []Message{{Role: "user", Content: "hello"}}}, func(event StreamEvent) { visible.WriteString(event.Delta) })
			if err != nil || result.Content != "Answer" || result.Reasoning != "Check" || result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 2 || !result.Usage.Available || strings.Contains(visible.String(), "secret") {
				t.Fatalf("incorrect non-streaming result: %#v, %v", result, err)
			}
		})
	}
}
