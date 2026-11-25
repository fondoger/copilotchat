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
const testClientID = "00000000-0000-4000-8000-000000000001"

func authFixture(t *testing.T, hook string) (*Plugin, wam.Profile) {
	t.Helper()
	dir := t.TempDir()
	m := Manifest{ID: "works", Name: "Works", Version: "1", Main: "index.js", Network: []string{"https://api.example.test", "https://region.example.test"}, WAMClientID: testClientID, WAMResource: testResource}
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
	return p, wam.Profile{ClientID: testClientID, Resource: testResource, Origins: []string{"https://api.example.test", "https://region.example.test"}}
}

func TestOnlyInteractiveHookCanRequestAccountPicker(t *testing.T) {
	p, grant := authFixture(t, `return h.wam.signIn()`)
	calls := 0
	saved, err := p.signIn(t.Context(), 37, func(_ context.Context, hwnd uintptr, g wam.Profile) (wam.Reference, error) {
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
	c, err := p.Client(saved)
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
	// Reordering an unchanged set of origins does not invalidate the account.
	p.Manifest.Network = []string{"https://region.example.test", "https://api.example.test", "https://api.example.test"}
	restored, err := p.Client(saved)
	if err != nil {
		t.Fatalf("restoring with equivalent origins: %v", err)
	}
	restored.Revoke()
	// Changing the approved registration invalidates the saved WAM account.
	p.Manifest.WAMClientID = testResource
	if _, err := p.Client(saved); err == nil {
		t.Fatal("restored account with a different registration")
	}
	// Restoring a client freezes the destinations which passed installation review.
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
		p, _ := authFixture(t, hook)
		if _, err := p.signIn(t.Context(), 37, unexpected); err == nil {
			t.Fatal("accepted hook", hook)
		}
	}
	p, _ := authFixture(t, `return h.wam.signIn()`)
	if _, err := p.signIn(t.Context(), 0, unexpected); err == nil {
		t.Fatal("no parent window")
	}
	p.Manifest.WAMResource = ""
	if _, err := p.signIn(t.Context(), 37, unexpected); err == nil {
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
	for _, field := range []string{`"unknownLoginPermission":true,`, `"wamResource":"` + testResource + `",`, `"wamClientId":"` + testClientID + `",`, `"wamClientId":"invalid","wamResource":"` + testResource + `",`, `"wamClientId":"` + testClientID + `","wamResource":"` + testResource + `",`} {
		data := bytes.Replace(m, []byte("{"), []byte("{"+field), 1)
		if err = os.WriteFile(filepath.Join(dir, "plugin.json"), data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(dir); err == nil {
			t.Fatal("accepted incomplete authentication declaration", field)
		}
	}
}
