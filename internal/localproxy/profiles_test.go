package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestProfileCatalogsAndAliasRouting(t *testing.T) {
	type call struct {
		account string
		request provider.RawRequest
	}
	calls := make(chan call, 8)
	client := func(account string) *rawClient {
		return &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
			calls <- call{account, r}
			return response("data: {\"usage\":{\"cache_read_input_tokens\":321}}\n\n"), nil
		}}
	}
	targets := map[string]Target{"github": {Client: client("github")}, "m365": {Client: client("m365")}}
	m := provider.Model{ID: "gpt-5.5", Name: "Upstream title", Endpoints: []string{"/responses", "/v1/messages", "/chat/completions"}, ThinkingEfforts: []string{"low", "medium", "high"}}
	profiles := map[string]Profile{
		"my-claude-code-profile": {ID: "claude-profile-id", Models: []Model{{ProviderID: "github", ID: "claude-work", Model: m}, {ProviderID: "m365", ID: "claude-work (2)", Model: m}}},
		"my-codex-profile":       {ID: "codex-profile-id", Models: []Model{{ProviderID: "github", ID: "gpt-5.5", Model: m}, {ProviderID: "m365", ID: "gpt-5.5 (2)", Model: m}}},
	}
	s := New(DefaultKey)
	s.Update(targets, profiles)
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	base := "http://" + s.Address()
	h := map[string]string{"Authorization": "Bearer " + DefaultKey, "X-Claude-Code-Session-Id": "same-session"}
	for path, aliases := range map[string][]string{
		"/my-claude-code-profile/v1/models": {"claude-work", "claude-work (2)"},
		"/my-codex-profile/models":          {"gpt-5.5", "gpt-5.5 (2)"},
	} {
		res := send(t, "GET", base+path, "", h)
		var catalog struct {
			Data []struct {
				ID, Name    string
				DisplayName string `json:"display_name"`
			}
		}
		if err := json.NewDecoder(res.Body).Decode(&catalog); err != nil {
			t.Fatal(err)
		}
		if len(catalog.Data) != len(aliases) {
			t.Fatal(catalog)
		}
		for i, model := range catalog.Data {
			name, display := aliases[i], aliases[i]
			if strings.HasSuffix(path, "/v1/models") {
				name, display = "", m.Name
			}
			if model.ID != aliases[i] || model.Name != name || model.DisplayName != display {
				t.Fatal("catalog leaked upstream identity", model)
			}
		}
	}
	res := send(t, "GET", base+"/my-codex-profile/codex/models", "", h)
	var native struct {
		Models []struct {
			Slug, Name   string
			DisplayName  string `json:"display_name"`
			Instructions string `json:"base_instructions"`
		}
	}
	if json.NewDecoder(res.Body).Decode(&native) != nil || len(native.Models) != 2 || native.Models[1].Slug != "gpt-5.5 (2)" || native.Models[1].DisplayName != native.Models[1].Slug || native.Models[1].Name != native.Models[1].Slug || native.Models[0].Instructions == "" {
		t.Fatal("invalid Codex catalog", native)
	}
	body := `{"model":"claude-work (2)","messages":[{"role":"user","content":"claude-work (2)"}],"system":[{"type":"text","text":"stable prefix","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"adaptive"},"future":{"large_integer":9007199254740993}}`
	res = send(t, "POST", base+"/my-claude-code-profile/v1/messages", body, h)
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != 200 || !strings.Contains(string(data), "cache_read_input_tokens\":321") {
		t.Fatal(res.Status, string(data))
	}
	got := <-calls
	if got.account != "m365" {
		t.Fatal("alias routed to wrong account")
	}
	var before, after map[string]json.RawMessage
	json.Unmarshal([]byte(body), &before)
	json.Unmarshal(got.request.Body, &after)
	if string(after["model"]) != `"gpt-5.5"` {
		t.Fatal("upstream received alias")
	}
	delete(before, "model")
	delete(after, "model")
	if !reflect.DeepEqual(before, after) {
		t.Fatal("non-routing JSON changed", string(got.request.Body))
	}
	firstSession := got.request.ConversationID
	res = send(t, "POST", base+"/my-codex-profile/responses", `{"model":"gpt-5.5 (2)","input":"Hello"}`, h)
	io.Copy(io.Discard, res.Body)
	got = <-calls
	if got.account != "m365" || got.request.ConversationID == firstSession {
		t.Fatal("profiles share routing/session state")
	}
	for _, path := range []string{"/my-codex-profile/v1/messages", "/missing/v1/messages"} {
		res = send(t, "POST", base+path, body, h)
		if res.StatusCode < 400 {
			t.Fatal("cross-profile alias accepted")
		}
	}
	// Identical aliases in different profiles remain independent.
	profiles["another"] = Profile{ID: "another-id", Models: []Model{{ProviderID: "github", ID: "claude-work (2)", Model: m}}}
	s.Update(targets, profiles)
	res = send(t, "POST", base+"/another/v1/messages", body, h)
	io.Copy(io.Discard, res.Body)
	if (<-calls).account != "github" {
		t.Fatal("same alias crossed profiles")
	}
	delete(targets, "m365")
	s.Update(targets, profiles)
	res = send(t, "POST", base+"/my-claude-code-profile/v1/messages", body, h)
	if res.StatusCode != 400 {
		t.Fatal("disconnected alias fell back to another provider")
	}
}

func TestProfileDeletionCancelsOnlyItsStreams(t *testing.T) {
	started, cancelled := make(chan string, 2), make(chan string, 2)
	client := &rawClient{fn: func(ctx context.Context, r provider.RawRequest) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			started <- r.ConversationID
			io.WriteString(writer, "data: first\n\n")
			<-ctx.Done()
			cancelled <- r.ConversationID
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}}
	s, base := fixture(t, client)
	targets := map[string]Target{"p1": {Client: client}}
	m := Model{ProviderID: "p1", ID: "shared", Model: provider.Model{ID: "real", Endpoint: "/responses"}}
	profiles := map[string]Profile{"a": {ID: "id-a", Models: []Model{m}}, "b": {ID: "id-b", Models: []Model{m}}}
	s.Update(targets, profiles)
	base = strings.TrimSuffix(base, "/fixture")
	h := map[string]string{"X-Api-Key": "fixture-key", "thread-id": "same-thread"}
	a := send(t, "POST", base+"/a/responses", `{"model":"shared"}`, h)
	defer a.Body.Close()
	first := <-started
	b := send(t, "POST", base+"/b/responses", `{"model":"shared"}`, h)
	defer b.Body.Close()
	second := <-started
	delete(profiles, "a")
	s.Update(targets, profiles)
	select {
	case id := <-cancelled:
		if id != first {
			t.Fatal("wrong profile cancelled")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deleted profile still streaming")
	}
	select {
	case <-cancelled:
		t.Fatal("other profile cancelled")
	default:
	}
	s.Close()
	select {
	case id := <-cancelled:
		if id != second {
			t.Fatal("wrong session")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("close did not cancel remaining request")
	}
}
