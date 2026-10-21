package desktop

import (
	"encoding/json"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
	"testing"
)

func TestForwardIDsIgnoreSavedAliasesAndSelections(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	var saved []store.ForwardModel
	if err := json.Unmarshal([]byte(`[{"providerId":"github.com","modelId":"gpt-5.5","alias":"claude-old","aliasMode":"manual","enabled":true},{"providerId":"work","modelId":"gpt-5.5","alias":"gpt-5.5 (2)","enabled":true}]`), &saved); err != nil {
		t.Fatal(err)
	}
	p.Models = saved
	models := a.forwardingModels(p)
	if len(models) != 2 || models[0].ID != "github-copilot/gpt-5.5" || models[1].ID != "m365-copilot/gpt-5.5" {
		t.Fatal(models)
	}
	encoded, _ := json.Marshal(p)
	if string(encoded) == "" || containsAliasField(encoded) {
		t.Fatal("obsolete aliases persisted", string(encoded))
	}
	a.setForwardEnabled(p, 0, false)
	models = a.forwardingModels(p)
	if len(models) != 1 || models[0].ID != "m365-copilot/gpt-5.5" {
		t.Fatal("selection renamed another provider", models)
	}
	a.setForwardEnabled(p, -1, true)
	if len(a.forwardingModels(p)) != 2 {
		t.Fatal("select all failed")
	}
}
func containsAliasField(data []byte) bool {
	var v map[string]json.RawMessage
	json.Unmarshal(data, &v)
	var rows []map[string]json.RawMessage
	json.Unmarshal(v["models"], &rows)
	for _, r := range rows {
		if r["alias"] != nil || r["aliasMode"] != nil {
			return true
		}
	}
	return false
}
func TestGatewayProviderOrderAndAccountIDs(t *testing.T) {
	a := profileTestApp(t)
	a.provider(store.DefaultProviderID).Login = "personal"
	a.provider("work").Kind = store.ProviderKindGitHub
	a.provider("work").Login = "work-user"
	p := a.selectedForwardProfile()
	// Saved model row order must not override saved provider priority.
	p.Models[0], p.Models[1] = p.Models[1], p.Models[0]
	models := a.forwardingModels(p)
	if models[0].ID != "github-copilot/personal/gpt-5.5" || models[1].ID != "github-copilot/work-user/gpt-5.5" {
		t.Fatal(models)
	}
	a.provider(store.DefaultProviderID).Disabled = true
	models = a.forwardingModels(p)
	if len(models) != 1 || models[0].ID != "github-copilot/work-user/gpt-5.5" {
		t.Fatal("offline account changed naming", models)
	}
}
func TestGatewayTableHasNoAliasEditor(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	tester := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.forwardingModelSettings(c, p) }) }, 600, 800)
	if _, ok := tester.Find("Alias work gpt-5.5"); ok {
		t.Fatal("alias editor remains")
	}
	settingsClick(t, tester, "Share github.com gpt-5.5")
	if p.Models[0].Enabled || len(a.forwardingModels(p)) != 1 {
		t.Fatal("checkbox did not save")
	}
	a.providers["work"].models = append(a.providers["work"].models, provider.Model{ID: "new", Endpoint: "/responses"})
	a.reconcileForwardModels()
	if len(p.Models) != 3 || !p.Models[2].Enabled {
		t.Fatal("new catalog model not enabled")
	}
}
