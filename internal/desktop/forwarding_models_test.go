package desktop

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestAutomaticAliasesUseEnabledModelsAndPreserveEdits(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	a.agentSetup.aliases = map[forwardModelKey]*aliasDraft{{"work", "gpt-5.5"}: {text: "gpt-5.5 (2)"}}
	if err := a.setForwardEnabled(p, 0, false); err != "" {
		t.Fatal(err)
	}
	if p.Models[1].Alias != "gpt-5.5" || a.agentSetup.aliases[forwardModelKey{"work", "gpt-5.5"}].text != "gpt-5.5" {
		t.Fatal("automatic alias and draft did not shed suffix")
	}
	a.setForwardEnabled(p, 0, true)
	if p.Models[1].Alias != "gpt-5.5 (2)" {
		t.Fatal("re-enabling did not resolve collision")
	}
	// Even an edit choosing the same default-looking name is manual.
	if err := a.setForwardAlias(p, 1, "gpt-5.5 (2)"); err != "" {
		t.Fatal(err)
	}
	a.setForwardEnabled(p, 0, false)
	a.state = settingsSavedState(t, a)
	p = a.selectedForwardProfile()
	a.reconcileForwardModels()
	if p.Models[1].Alias != "gpt-5.5 (2)" || p.Models[1].AliasMode != "manual" {
		t.Fatal("manual alias was reallocated after restart")
	}
}

func TestManualAliasCollisionOnEnableIsAtomic(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	a.setForwardAlias(p, 0, "custom")
	a.setForwardEnabled(p, 1, false)
	if err := a.setForwardAlias(p, 1, "custom"); err != "" {
		t.Fatal("disabled model unnecessarily reserved a public alias", err)
	}
	for _, index := range []int{1, -1} {
		before := slices.Clone(p.Models)
		if err := a.setForwardEnabled(p, index, true); err == "" || !slices.Equal(before, p.Models) {
			t.Fatal("conflicting manual aliases enabled or partially applied")
		}
	}
	a.setForwardEnabled(p, 0, false)
	if err := a.setForwardEnabled(p, 1, true); err != "" || p.Models[1].Alias != "custom" {
		t.Fatal("disabled manual alias blocked enabling", err)
	}
}

func TestAliasesHandleModelIDsWithSuffixesAndCatalogRefresh(t *testing.T) {
	a := profileTestApp(t)
	a.providers["work"].models = append(a.providers["work"].models, provider.Model{ID: "gpt-5.5 (2)", Endpoint: "/responses"})
	a.reconcileForwardModels()
	p := a.selectedForwardProfile()
	seen := map[string]bool{}
	for _, m := range a.forwardingModels(p) {
		if seen[m.Alias] {
			t.Fatal("literal suffix collided", m.Alias)
		}
		seen[m.Alias] = true
	}
	a.provider(store.DefaultProviderID).Disabled = true
	a.providers[store.DefaultProviderID].models = nil
	a.reconcileForwardModels()
	if p.Models[1].Alias != "gpt-5.5 (2)" {
		t.Fatal("temporary provider availability renumbered aliases")
	}
	a.setForwardEnabled(p, 0, false)
	if p.Models[1].Alias != "gpt-5.5" || p.Models[2].Alias != "gpt-5.5 (2)" {
		t.Fatal("selection did not free automatic aliases", p.Models)
	}
}

func TestModelCheckboxUpdatesVisibleAutomaticAlias(t *testing.T) {
	a := profileTestApp(t)
	p := a.selectedForwardProfile()
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Children(func() { a.forwardingModelSettings(c, p) })
	}, 600, 800)
	settingsClick(t, tester, "Share github.com gpt-5.5")
	if p.Models[1].Alias != "gpt-5.5" || a.agentSetup.aliases[forwardModelKey{"work", "gpt-5.5"}].text != "gpt-5.5" {
		t.Fatal("checkbox did not update model and visible alias")
	}
}
