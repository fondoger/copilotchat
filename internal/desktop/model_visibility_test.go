package desktop

import (
	"reflect"
	"slices"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestBulkModelVisibilityPersistsAndPreservesAbsentHiddenModels(t *testing.T) {
	a := providerTestApp(t)
	p := a.provider("work")
	p.DisabledModels = []string{"temporarily-absent", "same"}
	a.setAllModelsEnabled("work", false)
	persisted := settingsSavedState(t, a).Settings.Providers
	if len(a.providerModels("work")) != 0 || !slices.Contains(persisted[1].DisabledModels, "temporarily-absent") || !slices.Contains(persisted[1].DisabledModels, "same") || !slices.Contains(persisted[1].DisabledModels, "work-only") {
		t.Fatal("deselect all failed to save current rows or lost an absent hidden model")
	}
	if len(persisted[0].DisabledModels) != 0 || len(a.providerModels(store.DefaultProviderID)) != 2 {
		t.Fatal("bulk edit affected another provider")
	}
	a.setAllModelsEnabled("work", true)
	persisted = settingsSavedState(t, a).Settings.Providers
	if len(a.providerModels("work")) != 2 || !reflect.DeepEqual(persisted[1].DisabledModels, []string{"temporarily-absent"}) {
		t.Fatal("select all did not persist or changed an absent model's preference")
	}
	a.providerState("work").models = append(a.providerState("work").models, provider.Model{ID: "new-model"}, provider.Model{ID: "temporarily-absent"})
	if got := a.providerModels("work"); len(got) != 3 || !modelEnabled(p, "new-model") || modelEnabled(p, "temporarily-absent") {
		t.Fatal("new and returning models did not retain their expected visibility")
	}
}

func TestDisabledProviderModelPreferencesRemainEditableWithoutEnablingRouting(t *testing.T) {
	a := providerTestApp(t)
	p := a.provider("work")
	p.Disabled = true
	if !modelEnabled(p, "same") || modelAllowed(p, "same") {
		t.Fatal("disabled provider mixed model preference with routing eligibility")
	}
	a.setModelEnabled("work", "same", false)
	if modelEnabled(p, "same") || !modelEnabled(p, "work-only") {
		t.Fatal("disabled provider's saved preferences could not be edited")
	}
	a.setAllModelsEnabled("work", true)
	persisted := settingsSavedState(t, a).Settings.Providers[1]
	if !persisted.Disabled || !modelEnabled(&persisted, "same") || modelAllowed(&persisted, "same") || len(a.providerModels("work")) != 0 || a.clientForProvider("work") != nil {
		t.Fatal("editing model preferences enabled a disabled provider")
	}
}
