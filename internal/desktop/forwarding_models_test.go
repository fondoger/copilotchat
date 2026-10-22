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

func TestGatewayIgnoresChatModelVisibility(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	a.setModelEnabled(store.DefaultProviderID, "gpt-5.5", false)
	if modelEnabled(a.provider(store.DefaultProviderID), "gpt-5.5") {
		t.Fatal("chat visibility did not change")
	}
	if status := a.forwardingModelStatus(p.Models[0]); status != "" || len(a.forwardingModels(p)) != 2 {
		t.Fatal("chat visibility leaked into Gateway", status)
	}
	tester := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.forwardingModelSettings(c, p) }) }, 600, 800)
	if _, ok := tester.Find("Share github.com gpt-5.5"); !ok || tester.HasText("Hidden in Providers") {
		t.Fatal("hidden chat model unavailable in Gateway table")
	}
	a.setForwardEnabled(p, 0, false)
	a.setModelEnabled(store.DefaultProviderID, "gpt-5.5", true)
	if len(a.forwardingModels(p)) != 1 || p.Models[0].Enabled {
		t.Fatal("chat visibility changed Gateway's independent selection")
	}
}

func TestGatewayTableExcludesDisabledProviders(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	a.setProviderEnabled("work", false)
	tester := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.forwardingModelSettings(c, p) }) }, 600, 800)
	if _, ok := tester.Find("Share work gpt-5.5"); ok || tester.HasText("Microsoft 365") || !tester.HasText("Models · 1 enabled") {
		t.Fatal("disabled provider remains in Gateway table")
	}
	if len(a.forwardingModels(p)) != 1 || !p.Models[1].Enabled {
		t.Fatal("disabled provider remains routable or lost saved selection")
	}
	a.provider("work").Disabled = false
	a.saveSettings()
	tester.Frame()
	if _, ok := tester.Find("Share work gpt-5.5"); !ok || len(a.forwardingModels(p)) != 2 {
		t.Fatal("re-enabled provider did not restore Gateway selection")
	}
	a.setProviderEnabled("work", false)
	a.setProviderEnabled(store.DefaultProviderID, false)
	tester.Frame()
	if !tester.HasText("Models · 0 enabled") || !tester.HasText("Sign in and load models in Providers first.") {
		t.Fatal("all-disabled Gateway did not show empty state")
	}
}
