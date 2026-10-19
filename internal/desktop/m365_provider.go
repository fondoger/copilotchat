package desktop

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/broker"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/cli"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/store"
)

func (a *app) m365BrokerHome(id string) (string, error) {
	if a.dataDir == "" || id == "" {
		return "", errors.New("could not locate the Microsoft sign-in profile")
	}
	// Provider IDs from saved state never become path components directly.
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
	return filepath.Abs(filepath.Join(a.dataDir, "microsoft-accounts", key))
}

func (a *app) m365Client(id string, entry m365SavedSignIn) (provider.Client, interface{ Revoke() }, error) {
	var account entra.Account
	var token microsoft.TokenSource
	var renewable interface{ Revoke() }
	switch entry.Mode {
	case "copilot-cli", "copilot-broker":
		if entry.CLI == nil {
			return nil, nil, errors.New("invalid Copilot CLI account; reconnect this provider")
		}
		var source *cli.Source
		var err error
		if entry.Mode == "copilot-broker" {
			var home string
			home, err = a.m365BrokerHome(id)
			if err == nil {
				source, err = cli.NewBrokerSource(*entry.CLI, home)
			}
		} else {
			source, err = cli.NewSource(*entry.CLI)
		}
		if err != nil {
			return nil, nil, err
		}
		account, token, renewable = entry.CLI.Account, source.Token, source
	default:
		return nil, nil, errors.New("unsupported Microsoft sign-in; reconnect this provider")
	}
	return microsoft.NewClient(token, provider.AccountInfo{Login: account.Username, Host: microsoft.Host}), renewable, nil
}

func (a *app) loadM365Provider(p store.Provider, r *providerRuntime) {
	entry, err := loadM365SignIn(a.credentials(), p.ID)
	if err == nil {
		r.client, r.m365Auth, err = a.m365Client(p.ID, entry)
	}
	if err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
		r.credentialNotice = "Could not load Microsoft sign-in: " + err.Error()
	}
}

func (a *app) startM365Login(id, status string) (*providerRuntime, context.Context, context.CancelFunc, uint64, bool) {
	p := a.provider(id)
	if p == nil || p.Kind != store.ProviderKindM365 || p.Disabled || a.quitting {
		return nil, nil, nil, 0, false
	}
	r := a.providerState(id)
	if r.authBusy || r.credentialsBusy {
		return nil, nil, nil, 0, false
	}
	r.authEpoch++
	ctx, cancel := context.WithCancel(context.Background())
	r.authBusy, r.authCancel, r.authCode, r.status = true, cancel, github.DeviceCode{}, status
	return r, ctx, cancel, r.authEpoch, true
}

func (a *app) failedM365Login(id string, epoch uint64, ctx context.Context, cancel context.CancelFunc, err error) {
	a.update(func() {
		defer cancel()
		if !a.currentProviderLogin(id, epoch, ctx) {
			return
		}
		r := a.providerState(id)
		r.authBusy, r.authCancel, r.authCode = false, nil, github.DeviceCode{}
		r.status = err.Error()
	})
}

func (a *app) finishM365Login(id string, epoch uint64, ctx context.Context, cancel context.CancelFunc, entry m365SavedSignIn) {
	a.update(func() {
		defer cancel()
		if !a.currentProviderLogin(id, epoch, ctx) {
			return
		}
		r := a.providerState(id)
		r.credentialsBusy = true
		defer func() { r.credentialsBusy = false }()
		if r.m365Auth != nil {
			r.m365Auth.Revoke()
			r.m365Auth = nil
		}
		client, source, err := a.m365Client(id, entry)
		if err != nil {
			r.authBusy, r.authCancel, r.authCode = false, nil, github.DeviceCode{}
			r.status = err.Error()
			return
		}
		secretErr := saveM365SignIn(a.credentials(), id, entry)
		if !a.currentProviderLogin(id, epoch, ctx) {
			if source != nil {
				source.Revoke()
			}
			if secretErr == nil {
				_ = a.clearM365Credentials(id)
			}
			return
		}
		a.cancelProviderModels(id)
		r.client, r.m365Auth, r.models, r.modelsLoaded = client, source, nil, false
		r.authBusy, r.authCancel, r.authCode = false, nil, github.DeviceCode{}
		r.status, r.credentialNotice = "Connected.", ""
		a.provider(id).Login = entry.CLI.Account.Username
		if secretErr != nil {
			r.credentialNotice = "Connected for this session. Could not save sign-in: " + secretErr.Error()
		}
		a.saveSettings()
		a.refreshProviderModels(id)
	})
}

// Some tenants disallow device-code or browser-redirect authorization, including
// policies requiring device-bound token protection. M365 therefore uses Windows
// WAM or an existing Copilot CLI sign-in, without falling back to those flows.
func (a *app) beginM365Login(id string) {
	_, ctx, cancel, epoch, ok := a.startM365Login(id, "Choose your work account in the Windows sign-in window…")
	if !ok {
		return
	}
	home, err := a.m365BrokerHome(id)
	if err != nil {
		a.failedM365Login(id, epoch, ctx, cancel, err)
		return
	}
	go func() {
		err := broker.SignIn(ctx, home)
		var reference cli.Reference
		if err == nil {
			reference, err = cli.SelectedAt(home)
		}
		if err == nil {
			var source *cli.Source
			source, err = cli.NewBrokerSource(reference, home)
			if err == nil {
				_, err = source.Token(ctx)
				source.Revoke()
			}
		}
		if ctx.Err() != nil {
			cancel()
			return
		}
		if err != nil {
			a.failedM365Login(id, epoch, ctx, cancel, err)
			return
		}
		a.finishM365Login(id, epoch, ctx, cancel, m365SavedSignIn{Version: 1, Mode: "copilot-broker", CLI: &reference})
	}()
}

func (a *app) connectM365CLI(id string) {
	_, ctx, cancel, epoch, ok := a.startM365Login(id, "Connecting your Copilot CLI work account…")
	if !ok {
		return
	}
	go func() {
		reference, err := cli.Selected()
		if err == nil {
			var source *cli.Source
			source, err = cli.NewSource(reference)
			if err == nil {
				checkCtx, checkCancel := context.WithTimeout(ctx, 65*time.Second)
				_, err = source.Token(checkCtx)
				checkCancel()
				source.Revoke()
			}
		}
		if ctx.Err() != nil {
			cancel()
			return
		}
		if err != nil {
			a.failedM365Login(id, epoch, ctx, cancel, err)
			return
		}
		a.finishM365Login(id, epoch, ctx, cancel, m365SavedSignIn{Version: 1, Mode: "copilot-cli", CLI: &reference})
	}()
}
