package desktop

import (
	"context"

	"github.com/fondoger/copilotchat/internal/desktop/doubaoweb"
	"github.com/fondoger/copilotchat/internal/desktop/webapi"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/provider/webchat"
	"github.com/fondoger/copilotchat/internal/store"
)

func supportsWebAPIMode(kind string) bool {
	return kind == store.ProviderKindDoubao || kind == store.ProviderKindClaudeWeb || kind == store.ProviderKindChatGPTWeb
}

func nativeProvider(p *store.Provider) bool {
	return p != nil && (!store.IsWebProvider(p.Kind) || supportsWebAPIMode(p.Kind) && p.ExperimentalAPIMode)
}

func (a *app) loadWebAPIProvider(id string) {
	p := a.provider(id)
	if p == nil || !supportsWebAPIMode(p.Kind) {
		return
	}
	r := a.providerState(id)
	gate := make(chan struct{}, 1)
	if p.Kind == store.ProviderKindDoubao {
		var client *doubao.Client
		client = doubao.NewClient(func(ctx context.Context) (doubao.Session, error) {
			lease, err := a.openWebAPISession(ctx, id, store.ProviderKindDoubao, client, gate)
			if err != nil {
				return nil, err
			}
			return &doubaoSession{Transport: lease.transport.(*doubaoweb.Transport), lease: lease}, nil
		})
		r.client = client
		r.models, _ = client.Models(context.Background())
		r.modelsLoaded = true
		return
	}
	kind := p.Kind
	var client *webchat.Client
	client = webchat.NewClient(kind, func(ctx context.Context) (webchat.Session, error) {
		lease, err := a.openWebAPISession(ctx, id, kind, client, gate)
		if err != nil {
			return nil, err
		}
		return &nativeWebSession{Transport: lease.transport.(*webapi.Transport), lease: lease}, nil
	})
	r.client = client
}

func (a *app) setExperimentalAPIMode(id string, enabled bool) {
	p := a.provider(id)
	if p == nil || !supportsWebAPIMode(p.Kind) || p.ExperimentalAPIMode == enabled {
		return
	}
	a.cancelProviderWork(id)
	p.ExperimentalAPIMode = enabled
	r := a.providerState(id)
	r.client, r.models = nil, nil
	r.modelsLoaded = false
	if enabled {
		a.loadWebAPIProvider(id)
	}
	if a.state.Settings.ActiveProviderID == id {
		a.state.Settings.Model = ""
		if enabled && p.Kind == store.ProviderKindDoubao {
			a.state.Settings.Model = doubao.ModelID
		}
		a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens = "", 0
	}
	a.saveSettings()
}

func (a *app) openWebAPILogin(id string) {
	p := a.provider(id)
	if p == nil || p.Disabled || !supportsWebAPIMode(p.Kind) {
		return
	}
	a.closeWebSettings()
	a.providerSettingsID = id
	a.webAPILoginID = id
	a.openSettings("Providers")
}

func (a *app) finishWebAPILogin() {
	id := a.webAPILoginID
	a.closeWebSettings()
	p := a.provider(id)
	if p != nil && p.ExperimentalAPIMode && p.Kind != store.ProviderKindDoubao {
		a.cancelProviderModels(id)
		a.invalidateProviderCatalog(id)
		a.refreshProviderModels(id)
	}
}

func (a *app) closeWebAPISessions(id string) {
	for s := range a.webAPISessions {
		if id == "" || s.providerID == id {
			s.Close()
		}
	}
}
