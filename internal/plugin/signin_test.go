package plugin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/fondoger/copilotchat/internal/wam"
)

const testResource = "00000000-0000-4000-8000-000000000002"

func authFixture(t *testing.T, hook string) (*Plugin, wam.Profile) {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{ID: "works", Name: "Works", Version: "1", Main: "index.js", Network: []string{"https://api.example.test"}, WAMResource: testResource}
	b, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "plugin.json"), b, 0600); err != nil {
		t.Fatal(err)
	}
	// A module can capture host at load. The login capability only becomes
	// available on that object after load, for one interactive invocation.
	src := `const h=host; if(h.wam) throw new Error('WAM exposed at module initialization');
export function catalog(){ return typeof h.wam }
export function inference(){ return typeof h.wam }
export function models(){ return typeof h.wam }
export function signIn(){` + hook + `}`
	if err = os.WriteFile(filepath.Join(dir, m.Main), []byte(src), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p, wam.Profile{ClientID: "00000000-0000-4000-8000-000000000001", Resource: testResource, Origins: []string{"https://api.example.test"}}
}

func TestOnlyInteractiveHookCanRequestAccountPicker(t *testing.T) {
	p, grant := authFixture(t, `return h.wam.signIn()`)
	calls := 0
	saved, err := p.signIn(t.Context(), 37, &grant, func(_ context.Context, hwnd uintptr, g wam.Profile) (wam.Reference, error) {
		calls++
		if hwnd != 37 || g.ClientID != grant.ClientID || g.Resource != grant.Resource {
			t.Fatal("incorrect broker registration")
		}
		b, _ := json.Marshal(g)
		return wam.Reference{ID: "opaque", ProfileHash: fmt.Sprintf("%x", sha256.Sum256(b)), Account: wam.Account{ID: "00000000-0000-4000-8000-000000000003", TenantID: "00000000-0000-4000-8000-000000000004", Username: "fixture@example.test"}}, nil
	})
	if err != nil || calls != 1 {
		t.Fatalf("sign-in: calls=%d, err=%v", calls, err)
	}
	c, err := p.Client(saved, &grant)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Revoke()
	account, err := c.Account(t.Context())
	if err != nil || account.Login != "fixture@example.test" {
		t.Fatalf("restored account: %+v %v", account, err)
	}
	for _, name := range []string{"catalog", "models", "inference"} {
		var capability string
		if err := p.call(t.Context(), name, nil, &capability); err != nil || capability != "undefined" {
			t.Fatalf("%s: exposed %q, %v", name, capability, err)
		}
	}
	// Restoring a client freezes the destinations which passed native approval.
	p.Manifest.Network[0] = "https://elsewhere.example.test"
	other, _ := url.Parse(p.Manifest.Network[0])
	if c.allowed(other) {
		t.Fatal("running client's destinations changed")
	}
}

func TestInvalidLoginCannotOpenPicker(t *testing.T) {
	unexpected := func(context.Context, uintptr, wam.Profile) (wam.Reference, error) {
		t.Fatal("broker invoked without a valid login")
		return wam.Reference{}, nil
	}
	for _, hook := range []string{`return {}`, `return h.wam.signIn({scope:'extra'})`, `h.wam.signIn();throw Error('cancelled')`, `return Promise.resolve(h.wam.signIn())`} {
		p, g := authFixture(t, hook)
		if _, err := p.signIn(t.Context(), 37, &g, unexpected); err == nil {
			t.Fatal("accepted hook", hook)
		}
	}
	p, g := authFixture(t, `return h.wam.signIn()`)
	for _, mutate := range []func(*wam.Profile){func(g *wam.Profile) { g.ClientID = "" }, func(g *wam.Profile) { g.Resource = g.ClientID }, func(g *wam.Profile) { g.Origins = []string{"https://other.example.test"} }} {
		other := g
		mutate(&other)
		if _, err := p.signIn(t.Context(), 37, &other, unexpected); err == nil {
			t.Fatal("invalid host registration accepted")
		}
	}
	if _, err := p.signIn(t.Context(), 37, nil, unexpected); err == nil {
		t.Fatal("manifest self-authorized")
	}
	if _, err := p.signIn(t.Context(), 0, &g, unexpected); err == nil {
		t.Fatal("no parent window")
	}
	p.Manifest.WAMResource = ""
	if _, err := p.signIn(t.Context(), 37, nil, unexpected); err == nil {
		t.Fatal("anonymous plugin opened picker")
	}
}

func TestManifestRejectsUnknownAuthAndMissingHook(t *testing.T) {
	dir := t.TempDir()
	fixture := filepath.Join("..", "..", "examples", "catalog-provider")
	m, err := os.ReadFile(filepath.Join(fixture, "plugin.json"))
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.ReadFile(filepath.Join(fixture, "index.js"))
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "index.js"), src, 0600); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"unknownLoginPermission":true,`, `"wamResource":"` + testResource + `",`} {
		data := bytes.Replace(m, []byte("{"), []byte("{"+field), 1)
		if err = os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(dir); err == nil {
			t.Fatal("accepted incomplete authentication declaration", field)
		}
	}
}
