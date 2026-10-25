package localproxy

import (
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestCodexCatalogSearchCapability(t *testing.T) {
	for _, test := range []struct {
		name  string
		model provider.Model
		want  bool
	}{
		{"opaque-gpt-alias", provider.Model{ID: "GPT-upstream", Endpoint: "/responses", Tools: true}, true},
		{"gpt-public-alias", provider.Model{ID: "grok-4.7", Endpoint: "/responses", Tools: true}, false},
		{"other-responses", provider.Model{ID: "mai-code", Endpoint: "/responses", Tools: true}, false},
		{"messages-tools", provider.Model{ID: "claude", Endpoint: "/v1/messages", Tools: true}, true},
		{"no-tools", provider.Model{ID: "gpt", Endpoint: "/responses"}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			models := []Model{{ID: test.name, Model: test.model}}
			rows := array(parsed(t, string(CodexCatalog(models)))["models"])
			if len(rows) != 1 {
				t.Fatal("routable model missing from catalog", rows)
			}
			row := obj(rows[0])
			if got, exists := row["supports_search_tool"].(bool); !exists || got != test.want {
				t.Fatal("incorrect search capability", row)
			}
			if row["supports_parallel_tool_calls"] != test.model.Tools {
				t.Fatal("ordinary tool capability changed", row)
			}
		})
	}
	models := []Model{{ID: "incompatible", Model: provider.Model{ID: "gpt", Endpoint: "/chat/completions", Tools: true}}}
	if rows := array(parsed(t, string(CodexCatalog(models)))["models"]); len(rows) != 0 {
		t.Fatal("catalog must exclude unroutable models", rows)
	}
}
