package broker

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type expectedCall struct {
	method string
	input  string
	reply  string
	err    error
}

type scriptedCaller struct {
	t     *testing.T
	calls []expectedCall
}

func (s *scriptedCaller) call(ctx context.Context, method string, params map[string]any, result any) error {
	s.t.Helper()
	if len(s.calls) == 0 {
		s.t.Fatalf("unexpected request %s", method)
	}
	expected := s.calls[0]
	s.calls = s.calls[1:]
	if method != expected.method {
		s.t.Fatalf("request %q, want %q", method, expected.method)
	}
	if expected.input != "" && params["input"] != expected.input {
		s.t.Fatalf("selected account %v, want %q", params["input"], expected.input)
	}
	if method == "session.accounts.login.begin" && (params["kind"] != "entra" || params["mode"] != "selectAccount") {
		s.t.Fatal("login did not explicitly use the Entra account selector")
	}
	if expected.err != nil {
		return expected.err
	}
	if result != nil {
		return json.Unmarshal([]byte(expected.reply), result)
	}
	return nil
}

const providersReply = `{"kind":"providers","items":[{"kind":"entra","available":true}]}`

func TestSignInSelectsMicrosoft365FromSharedEntraIdentity(t *testing.T) {
	s := &scriptedCaller{t: t, calls: []expectedCall{
		{method: "session.accounts.enumerate", reply: providersReply},
		{method: "session.accounts.login.begin", reply: `{"flowId":"flow-1","step":{"kind":"needs-interaction"}}`},
		{method: "session.accounts.login.advance", reply: `{"kind":"completed","result":{"status":"needs-account-selection","accounts":[{"kind":"entraEmu","selectionId":"emu","host":"https://github.com"},{"kind":"loki","selectionId":"m365","host":"` + gateway + `","login":"test@example.com"}]}}`},
		{method: "session.accounts.login.advance", input: "m365", reply: `{"kind":"completed","result":{"status":"completed","host":"` + gateway + `","login":"test@example.com"}}`},
	}}
	if err := signIn(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatal("sign-in did not finish")
	}
}

func TestSignInRejectsUnsafeOrUnrelatedResults(t *testing.T) {
	for name, step := range map[string]string{
		"plaintext":             `{"kind":"completed","result":{"status":"needs-plaintext-consent"}}`,
		"github only":           `{"kind":"completed","result":{"status":"needs-account-selection","accounts":[{"kind":"entraEmu","selectionId":"emu","host":"https://github.com"}]}}`,
		"wrong gateway":         `{"kind":"completed","result":{"status":"completed","host":"https://other.example.com","login":"test@example.com"}}`,
		"consumer browser flow": `{"kind":"open-url","url":"https://example.com"}`,
		"missing login":         `{"kind":"completed","result":{"status":"completed","host":"` + gateway + `"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := &scriptedCaller{t: t, calls: []expectedCall{
				{method: "session.accounts.enumerate", reply: providersReply},
				{method: "session.accounts.login.begin", reply: `{"flowId":"flow-1","step":` + step + `}`},
				{method: "session.accounts.login.cancel"},
			}}
			if err := signIn(context.Background(), s); err == nil {
				t.Fatal("unsafe result was accepted")
			}
			if len(s.calls) != 0 {
				t.Fatal("unfinished broker flow was not cancelled")
			}
		})
	}
}

func TestSignInCancellationReleasesFlow(t *testing.T) {
	s := &scriptedCaller{t: t, calls: []expectedCall{
		{method: "session.accounts.enumerate", reply: providersReply},
		{method: "session.accounts.login.begin", reply: `{"flowId":"flow-1","step":{"kind":"needs-interaction"}}`},
		{method: "session.accounts.login.advance", err: context.Canceled},
		{method: "session.accounts.login.cancel"},
	}}
	if err := signIn(context.Background(), s); !errors.Is(err, context.Canceled) {
		t.Fatalf("got %v", err)
	}
	if len(s.calls) != 0 {
		t.Fatal("cancel did not release broker")
	}
}

func TestRefreshOnlyListsModelsForSelectedM365Account(t *testing.T) {
	s := &scriptedCaller{t: t, calls: []expectedCall{
		{method: "session.accounts.get", reply: `{"kind":"activeAccount","account":{"kind":"loki","host":"` + gateway + `","login":"test@example.com"}}`},
		{method: "session.model.list", reply: `{"models":[]}`},
	}}
	if err := refresh(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	if len(s.calls) != 0 {
		t.Fatal("catalog was not refreshed")
	}
	s = &scriptedCaller{t: t, calls: []expectedCall{
		{method: "session.accounts.get", reply: `{"kind":"activeAccount","account":{"kind":"githubDotCom","host":"https://github.com"}}`},
	}}
	if err := refresh(context.Background(), s); err == nil {
		t.Fatal("refresh accepted unrelated active account")
	}
}

func TestSignInCannotChangeNormalCLIProfile(t *testing.T) {
	root := t.TempDir()
	t.Setenv("COPILOT_HOME", root)
	if err := isolatedHome(root); err == nil {
		t.Fatal("sign-in accepted normal CLI profile")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if err := isolatedHome(filepath.Join(home, ".copilot")); err == nil {
		t.Fatal("sign-in accepted default CLI profile")
	}
	if err := isolatedHome(t.TempDir()); err != nil {
		t.Fatal(err)
	}
}

func TestEnvironmentPreservesSystemButRemovesAuthOverrides(t *testing.T) {
	got := environment([]string{"PATH=/bin", "GH_TOKEN=secret", "GITHUB_TOKEN=secret", "COPILOT_HOME=old", "COPILOT_PROVIDER_BASE_URL=https://other", "COPILOT_LOKI_TOKEN_COMMAND=command", "COPILOT_SDK_AUTH_TOKEN=secret"}, "/isolated")
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "other") || strings.Contains(joined, "old") || !strings.Contains(joined, "PATH=/bin") || !strings.Contains(joined, "COPILOT_HOME=/isolated") {
		t.Fatalf("unexpected environment: %v", got)
	}
}

func TestInstalledCLIAvailability(t *testing.T) {
	if os.Getenv("COPILOTCHAT_TEST_BROKER") != "1" {
		t.Skip("set COPILOTCHAT_TEST_BROKER=1 for read-only installed CLI protocol check")
	}
	if err := Available(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
}
