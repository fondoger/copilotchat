package localproxy

import (
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestCodexCatalogSearchCapabilityTracksTools(t *testing.T) {
	models := []Model{
		{ID: "responses-tools", Model: provider.Model{ID: "a", Endpoint: "/responses", Tools: true}},
		{ID: "messages-tools", Model: provider.Model{ID: "b", Endpoint: "/v1/messages", Tools: true}},
		{ID: "no-tools", Model: provider.Model{ID: "c", Endpoint: "/responses"}},
		{ID: "incompatible", Model: provider.Model{ID: "d", Endpoint: "/chat/completions", Tools: true}},
	}
	rows := array(parsed(t, string(CodexCatalog(models)))["models"])
	if len(rows) != 3 {
		t.Fatal("catalog must contain only routable models", rows)
	}
	for i, value := range rows {
		row := obj(value)
		got, exists := row["supports_search_tool"].(bool)
		if !exists || got != models[i].Model.Tools {
			t.Fatal("search capability must track the model's tool support", row)
		}
	}
}
