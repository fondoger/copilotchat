package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestGeneratedModelIDsAndAccountCollisions(t *testing.T) {
	identities := []ProviderIdentity{{ID: "g1", Kind: "github-copilot", Name: "GitHub Copilot", Login: "qinyuan"}, {ID: "m1", Kind: "m365-copilot", Login: "work@example.com"}}
	models := []Model{{ProviderID: "m1", Model: provider.Model{ID: "gpt-5.5"}}, {ProviderID: "g1", Model: provider.Model{ID: "gpt-5.5"}}}
	named := NameModels(models, identities)
	if named[0].ID != "github-copilot/gpt-5.5" || named[1].ID != "m365-copilot/gpt-5.5" || !slices.Contains(named[0].Routes, "github-copilot/qinyuan/gpt-5.5") {
		t.Fatal(named)
	}
	identities = append(identities, ProviderIdentity{ID: "g2", Kind: "github-copilot", Login: "other"})
	models = append(models, Model{ProviderID: "g2", Model: provider.Model{ID: "gpt-5.5"}})
	named = NameModels(models, identities)
	if named[0].ID != "github-copilot/qinyuan/gpt-5.5" || named[2].ID != "github-copilot/other/gpt-5.5" {
		t.Fatal(named)
	}
	identities[2].Login = "qinyuan"
	named = NameModels(models, identities)
	if named[0].ID == named[2].ID || !slices.Contains(named[2].Routes, "github-copilot/qinyuan/gpt-5.5") {
		t.Fatal("duplicate login needs stable disambiguation", named)
	}
	identities[0].Login, identities[2].Login = "", ""
	named = NameModels(models, identities)
	if named[0].ID == named[2].ID || !strings.Contains(named[0].ID, "/account-") {
		t.Fatal(named)
	}
}

func TestShortModelRoutesStayOnFirstProvider(t *testing.T) {
	calls := make(chan string, 20)
	client := func(id string) *rawClient {
		return &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
			if got := parsed(t, string(r.Body))["model"]; got != "gpt-5.5" {
				t.Errorf("upstream received public ID or context suffix: %v", got)
			}
			calls <- id + ":" + r.ConversationID
			return response("data: [DONE]\n\n"), nil
		}}
	}
	identities := []ProviderIdentity{{ID: "first", Kind: "github-copilot", Login: "qinyuan"}, {ID: "second", Kind: "github-copilot", Login: "work"}}
	named := NameModels([]Model{{ProviderID: "second", Model: provider.Model{ID: "gpt-5.5", Endpoint: "/responses"}}, {ProviderID: "first", Model: provider.Model{ID: "gpt-5.5", Endpoint: "/responses"}}}, identities)
	targets := map[string]Target{"second": {Client: client("second")}, "first": {Client: client("first")}}
	s, base := fixture(t, client("unused"))
	profiles := map[string]Profile{"fixture": {ID: "fixture-id", Models: named}}
	h := map[string]string{"X-Api-Key": "fixture-key", "X-Claude-Code-Session-Id": "same-session"}
	var conversation string
	for i := 0; i < 5; i++ {
		s.Update(targets, profiles)
		id := "github-copilot/gpt-5.5"
		if i%2 == 1 {
			id += "[1m]"
		}
		data, _ := json.Marshal(object{"model": id})
		res := send(t, "POST", base+"/responses", string(data), h)
		io.Copy(io.Discard, res.Body)
		got := <-calls
		if !strings.HasPrefix(got, "first:") || (conversation != "" && got != conversation) {
			t.Fatal("unstable account/cache affinity", got, conversation)
		}
		conversation = got
	}
	for id, want := range map[string]string{"github-copilot/qinyuan/gpt-5.5": "first:", "github-copilot/work/gpt-5.5": "second:", "github-copilot/qinyuan/gpt-5.5[1m]": "first:", "github-copilot/work/gpt-5.5[1m]": "second:"} {
		data, _ := json.Marshal(object{"model": id})
		res := send(t, "POST", base+"/responses", string(data), h)
		io.Copy(io.Discard, res.Body)
		if got := <-calls; !strings.HasPrefix(got, want) {
			t.Fatal(got)
		}
	}
	for _, old := range []string{"gpt-5.5", "claude-custom", "gpt-5.5 (2)"} {
		data, _ := json.Marshal(object{"model": old})
		if res := send(t, "POST", base+"/responses", string(data), h); res.StatusCode != 400 {
			t.Fatal("obsolete alias accepted", old)
		}
	}
	// Even a single configured account accepts both forms.
	profiles["fixture"] = Profile{ID: "fixture-id", Models: NameModels(named[:1], identities[:1])}
	s.Update(targets, profiles)
	for _, id := range []string{"github-copilot/gpt-5.5", "github-copilot/qinyuan/gpt-5.5", "github-copilot/gpt-5.5[1m]", "github-copilot/qinyuan/gpt-5.5[1m]"} {
		data, _ := json.Marshal(object{"model": id})
		res := send(t, "POST", base+"/responses", string(data), h)
		io.Copy(io.Discard, res.Body)
		if got := <-calls; !strings.HasPrefix(got, "first:") {
			t.Fatal(got)
		}
	}
}
