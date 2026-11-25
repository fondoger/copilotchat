package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/MoeForge/moejs"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/wam"
)

type savedAccount struct {
	Plugin    string        `json:"plugin"`
	Reference wam.Reference `json:"reference"`
}

// ValidateGrant compares the plugin's request with the independent registration
// entered in the native provider settings. JS cannot issue or modify a grant.
func (p *Plugin) ValidateGrant(grant *wam.Profile) error {
	if p.Manifest.WAMResource == "" {
		if grant == nil {
			return nil
		}
		return errors.New("this plugin does not declare Windows sign-in")
	}
	if grant == nil {
		return errors.New("save the Windows sign-in registration in Providers first")
	}
	if err := grant.Validate(); err != nil {
		return err
	}
	if !strings.EqualFold(grant.Resource, p.Manifest.WAMResource) {
		return errors.New("the approved API resource does not match the one requested by this plugin")
	}
	for _, origin := range p.Manifest.Network {
		if !slices.Contains(grant.Origins, origin) {
			return fmt.Errorf("this registration does not authorize %s", origin)
		}
	}
	return nil
}

func (p *Plugin) SignIn(ctx context.Context, hwnd uintptr, grant *wam.Profile) ([]byte, error) {
	return p.signIn(ctx, hwnd, grant, wam.SignIn)
}

func (p *Plugin) signIn(ctx context.Context, hwnd uintptr, grant *wam.Profile, picker func(context.Context, uintptr, wam.Profile) (wam.Reference, error)) ([]byte, error) {
	if p.Manifest.WAMResource == "" {
		return nil, errors.New("this plugin does not request Windows sign-in")
	}
	if err := p.ValidateGrant(grant); err != nil {
		return nil, err
	}
	if hwnd == 0 {
		return nil, errors.New("Windows sign-in requires a parent window")
	}
	// Only a native Sign in gesture enters this VM. Finish JS before opening
	// the picker: broker UI does not consume the script's execution budget.
	err := p.withRuntime(ctx, func(rt *moejs.Runtime, host moejs.Value) error {
		intent, err := rt.FromGo(map[string]any{})
		if err != nil {
			return err
		}
		request := rt.Function("signIn", 0, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
			if len(args) != 0 {
				return moejs.Undefined(), errors.New("host.wam.signIn takes no arguments")
			}
			return intent, nil
		})
		api, err := rt.FromGo(map[string]any{"signIn": request})
		if err != nil {
			return err
		}
		r := rt.Realm()
		if err := host.AsObject().CreateDataPropertyOrThrow(r, r.KeyFromGoString("wam"), api); err != nil {
			return err
		}
		fn, _ := rt.Export("signIn")
		result, err := rt.CallFunction(fn, moejs.Undefined())
		if err != nil {
			return err
		}
		if !result.IsObject() || result.AsObject() != intent.AsObject() {
			return errors.New("signIn must return host.wam.signIn()")
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	ref, err := picker(ctx, hwnd, *grant)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return json.Marshal(savedAccount{p.Manifest.ID, ref})
}

type credential interface {
	Token(context.Context) (string, error)
	Revoke()
}

type Client struct {
	*provider.Engine
	plugin     *Plugin
	origins    []string // private snapshot of the validated destinations, shared by catalog and inference
	http       *http.Client
	account    provider.AccountInfo
	source     credential
	lifetime   context.Context
	revoke     context.CancelFunc
	mu         sync.RWMutex
	catalogURL string
	models     map[string]provider.Model
}

var _ provider.Client = (*Client)(nil)
var _ provider.RawClient = (*Client)(nil)

func (p *Plugin) Client(saved []byte, grant *wam.Profile) (*Client, error) {
	var source credential
	account := provider.AccountInfo{Host: strings.TrimPrefix(p.Manifest.Network[0], "https://")}
	if p.Manifest.WAMResource != "" {
		if err := p.ValidateGrant(grant); err != nil {
			return nil, err
		}
		var s savedAccount
		if len(saved) == 0 || json.Unmarshal(saved, &s) != nil || s.Plugin != p.Manifest.ID {
			return nil, errors.New("sign in to connect this plugin account")
		}
		var err error
		source, err = wam.NewSource(s.Reference, *grant)
		if err != nil {
			return nil, err
		}
		a := s.Reference.Account
		account.ID = strings.ToLower(a.TenantID + "." + a.ID)
		account.Login = a.Username
	} else if len(saved) != 0 || grant != nil {
		return nil, errors.New("this plugin's authentication has changed; reconnect the provider")
	}
	return newClient(p, account, source), nil
}

func newClient(p *Plugin, account provider.AccountInfo, source credential) *Client {
	life, cancel := context.WithCancel(context.Background())
	c := &Client{plugin: p, origins: slices.Clone(p.Manifest.Network), source: source, account: account, lifetime: life, revoke: cancel,
		http: &http.Client{Transport: http.DefaultTransport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	c.Engine = provider.NewEngine(c)
	return c
}
func (c *Client) Host() string { return c.account.Host }
func (c *Client) Account(ctx context.Context) (provider.AccountInfo, error) {
	if err := ctx.Err(); err != nil {
		return provider.AccountInfo{}, err
	}
	if err := c.lifetime.Err(); err != nil {
		return provider.AccountInfo{}, err
	}
	return c.account, nil
}
func (c *Client) Revoke() {
	c.revoke()
	if c.source != nil {
		c.source.Revoke()
	}
}
