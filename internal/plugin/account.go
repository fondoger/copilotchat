package plugin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/wam"
	"net/http"
	"slices"
	"strings"
	"sync"
)

type savedAccount struct {
	Plugin    string        `json:"plugin"`
	Profile   string        `json:"profile"`
	Reference wam.Reference `json:"reference"`
}

// Resolve the registration on the host, never from code supplied by a plugin.
func (p *Plugin) profile() (wam.Profile, error) {
	prof, err := wam.ReadProfile(p.Manifest.WAMProfile)
	if err != nil {
		return prof, err
	}
	for _, origin := range p.Manifest.Network {
		if !slices.Contains(prof.Origins, origin) {
			return prof, fmt.Errorf("WAM profile %q does not authorize %s", p.Manifest.WAMProfile, origin)
		}
	}
	return prof, nil
}

func (p *Plugin) SignIn(ctx context.Context, hwnd uintptr) ([]byte, error) {
	if p.Manifest.WAMProfile == "" {
		return nil, nil
	}
	prof, err := p.profile()
	if err != nil {
		return nil, err
	}
	ref, err := wam.SignIn(ctx, hwnd, prof)
	if err != nil {
		return nil, err
	}
	return json.Marshal(savedAccount{p.Manifest.ID, p.Manifest.WAMProfile, ref})
}

type credential interface {
	Token(context.Context) (string, error)
	Revoke()
}

type Client struct {
	*provider.Engine
	plugin     *Plugin
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

func (p *Plugin) Client(saved []byte) (*Client, error) {
	var source credential
	account := provider.AccountInfo{Host: strings.TrimPrefix(p.Manifest.Network[0], "https://")}
	if p.Manifest.WAMProfile != "" {
		var s savedAccount
		if len(saved) == 0 || json.Unmarshal(saved, &s) != nil || s.Plugin != p.Manifest.ID || s.Profile != p.Manifest.WAMProfile {
			return nil, errors.New("sign in to connect this plugin account")
		}
		prof, err := p.profile()
		if err != nil {
			return nil, err
		}
		source, err = wam.NewSource(s.Reference, prof)
		if err != nil {
			return nil, err
		}
		a := s.Reference.Account
		account.ID = strings.ToLower(a.TenantID + "." + a.ID)
		account.Login = a.Username
	} else if len(saved) != 0 {
		return nil, errors.New("this plugin's authentication has changed; reconnect the provider")
	}
	return newClient(p, account, source), nil
}

func newClient(p *Plugin, account provider.AccountInfo, source credential) *Client {
	life, cancel := context.WithCancel(context.Background())
	c := &Client{plugin: p, source: source, account: account, lifetime: life, revoke: cancel,
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
