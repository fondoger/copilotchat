package desktop

import (
	"context"

	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

func nativeProvider(p *store.Provider) bool {
	return p != nil && (!store.IsWebProvider(p.Kind) || p.Kind == store.ProviderKindDoubao && p.ExperimentalAPIMode)
}

func (a *app) loadDoubaoProvider(id string) {
	r := a.providerState(id)
	gate := make(chan struct{}, 1)
	var client *doubao.Client
	client = doubao.NewClient(func(ctx context.Context) (doubao.Session, error) {
		return a.openDoubaoSession(ctx, id, client, gate)
	})
	r.client = client
	r.models, _ = client.Models(context.Background())
	r.modelsLoaded = true
}

func (a *app) setExperimentalAPIMode(id string, enabled bool) {
	p := a.provider(id)
	if p == nil || p.Kind != store.ProviderKindDoubao || p.ExperimentalAPIMode == enabled {
		return
	}
	a.cancelProviderWork(id)
	p.ExperimentalAPIMode = enabled
	r := a.providerState(id)
	r.client, r.models = nil, nil
	r.modelsLoaded = false
	if enabled {
		a.loadDoubaoProvider(id)
	}
	if a.state.Settings.ActiveProviderID == id {
		a.state.Settings.Model = ""
		if enabled {
			a.state.Settings.Model = doubao.ModelID
		}
		a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens = "", 0
	}
	a.saveSettings()
}

func (a *app) openDoubaoLogin(id string) {
	p := a.provider(id)
	if p == nil || p.Disabled || p.Kind != store.ProviderKindDoubao {
		return
	}
	a.closeWebSettings()
	a.providerSettingsID = id
	a.doubaoLoginID = id
	a.openSettings("Providers")
}

func (a *app) finishDoubaoLogin() {
	a.closeWebSettings()
	a.doubaoLoginID = ""
}

func (a *app) closeDoubaoSessions(id string) {
	for s := range a.doubaoSessions {
		if id == "" || s.providerID == id {
			s.Close()
		}
	}
}
