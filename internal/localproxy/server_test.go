package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

type rawClient struct {
	fn func(context.Context, provider.RawRequest) (*http.Response, error)
}

func (c *rawClient) Forward(ctx context.Context, r provider.RawRequest) (*http.Response, error) {
	return c.fn(ctx, r)
}
func response(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body))}
}
func fixture(t *testing.T, client *rawClient) (*Server, string) {
	t.Helper()
	s := New("fixture-key")
	s.Update(map[string]Target{"p1": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: []Model{
		{ProviderID: "p1", ID: "claude-fixture", Model: provider.Model{ID: "claude-fixture", Name: "Claude Fixture", Endpoint: "/v1/messages"}},
		{ProviderID: "p1", ID: "gpt-fixture", Model: provider.Model{ID: "gpt-fixture", Endpoint: "/responses"}},
	}}})
	if err := s.Start(0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, "http://" + s.Address() + "/fixture"
}
func send(t *testing.T, method, url, body string, headers map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { res.Body.Close() })
	return res
}

func TestRoutingAuthenticationAndRawProtocol(t *testing.T) {
	var requests []provider.RawRequest
	var requestsMu sync.Mutex
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		requestsMu.Lock()
		requests = append(requests, r)
		requestsMu.Unlock()
		return response("event: message_delta\ndata: {\"usage\":{\"cache_read_input_tokens\":321}}\n\n"), nil
	}}
	_, base := fixture(t, client)
	body := `{"model":"claude-fixture","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"OK"}]}],"system":[{"type":"text","text":"prefix","cache_control":{"type":"ephemeral"}}],"thinking":{"type":"adaptive"},"future_field":42}`
	for _, key := range []string{"", "incorrect"} {
		res := send(t, "POST", base+"/v1/messages", body, map[string]string{"X-Api-Key": key})
		if res.StatusCode != 401 {
			t.Fatal(res.Status)
		}
	}
	headers := map[string]string{"Authorization": "Bearer fixture-key", "X-Claude-Code-Session-Id": "session-A", "Anthropic-Beta": "test-beta"}
	for i := 0; i < 2; i++ {
		res := send(t, "POST", base+"/v1/messages?beta=true", body, headers)
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || !strings.Contains(string(data), "cache_read_input_tokens") {
			t.Fatal(res.Status, string(data))
		}
	}
	requestsMu.Lock()
	if len(requests) != 2 || string(requests[0].Body) != body || requests[0].RawQuery != "beta=true" || requests[0].ConversationID != requests[1].ConversationID || !requests[0].AgentInitiated || requests[0].Header.Get("Anthropic-Beta") != "test-beta" {
		t.Fatal("raw forwarding or session continuity failed")
	}
	requestsMu.Unlock()
	headers["X-Claude-Code-Session-Id"] = "session-B"
	res := send(t, "POST", base+"/v1/messages", body, headers)
	io.Copy(io.Discard, res.Body)
	requestsMu.Lock()
	if requests[2].ConversationID == requests[0].ConversationID {
		t.Fatal("sessions share an ID")
	}
	requestsMu.Unlock()
	for path, code := range map[string]int{"/arbitrary": 404, "/v1/responses": 400, "/chat/completions": 400} {
		res := send(t, "POST", base+path, body, headers)
		if res.StatusCode != code {
			t.Fatal(path, res.Status)
		}
	}
	res = send(t, "GET", base+"/v1/models?limit=1000", "", headers)
	var models struct {
		Data    []provider.Model `json:"data"`
		HasMore bool             `json:"has_more"`
	}
	if json.NewDecoder(res.Body).Decode(&models) != nil || len(models.Data) != 2 || models.Data[0].ID != "claude-fixture" || models.HasMore {
		t.Fatal(models)
	}
	headers["Origin"] = "https://example.com"
	if send(t, "POST", base+"/v1/messages", body, headers).StatusCode != 403 {
		t.Fatal("browser origin allowed")
	}
}

func TestSessionMapping(t *testing.T) {
	h := http.Header{}
	h.Set("session-id", "codex-thread")
	a := conversationID("p1", h)
	if a != conversationID("p1", h) || a == conversationID("p2", h) {
		t.Fatal("provider/session isolation")
	}
	h.Set("session-id", "other-thread")
	if a == conversationID("p1", h) {
		t.Fatal("Codex threads share ID")
	}
	if conversationID("p1", http.Header{}) == conversationID("p1", http.Header{}) {
		t.Fatal("global fallback")
	}
}

func TestResponsesAndImageHints(t *testing.T) {
	calls := make(chan provider.RawRequest, 4)
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		return response("data: [DONE]\n\n"), nil
	}}
	_, base := fixture(t, client)
	for _, test := range []struct {
		body          string
		agent, vision bool
	}{
		{`{"model":"gpt-fixture","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"describe an image"}]}]}`, false, false},
		{`{"model":"gpt-fixture","input":[{"type":"message","role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,fixture"}]}]}`, false, true},
		{`{"model":"gpt-fixture","input":[{"type":"function_call_output","call_id":"1","output":"result"}]}`, true, false},
	} {
		res := send(t, "POST", base+"/v1/responses", test.body, map[string]string{"Authorization": "Bearer fixture-key", "thread-id": "codex-thread"})
		io.Copy(io.Discard, res.Body)
		if res.StatusCode != 200 {
			t.Fatal(res.Status)
		}
		got := <-calls
		if got.Endpoint != "/responses" || got.AgentInitiated != test.agent || got.Vision != test.vision || string(got.Body) != test.body {
			t.Fatal("Responses metadata or body changed")
		}
	}
	if !hasImages(json.RawMessage(`[{"type":"tool_result","content":[{"type":"image","source":{}}]}]`)) {
		t.Fatal("tool image was lost")
	}
}

func TestRemoteCompactionRoutesAliasesAndPreservesProtocol(t *testing.T) {
	calls := make(chan provider.RawRequest, 4)
	reply := "{\"id\":\"cmp_test\",\"object\":\"response.compaction\",\"output\":[{\"type\":\"compaction\",\"encrypted_content\":\"opaque\"}],\"usage\":{\"input_tokens_details\":{\"cached_tokens\":321}}}"
	client := &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
		calls <- r
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
	}}
	s, base := fixture(t, client)
	s.Update(map[string]Target{"p1": {Client: client}}, map[string]Profile{"fixture": {ID: "fixture-id", Models: []Model{
		{ProviderID: "p1", ID: "my-codex", Model: provider.Model{ID: "upstream-model", Endpoint: "/responses"}},
		{ProviderID: "p1", ID: "claude-only", Model: provider.Model{ID: "claude-only", Endpoint: "/v1/messages"}},
	}}})
	body := "{\"model\":\"my-codex\",\"input\":[{\"type\":\"compaction\",\"encrypted_content\":\"keep-me\"}],\"instructions\":\"preserve\",\"unknown\":9007199254740993}"
	headers := map[string]string{"Authorization": "Bearer fixture-key", "thread-id": "compacted-thread"}
	var conversation string
	for _, path := range []string{"/responses", "/responses/compact", "/v1/responses/compact"} {
		res := send(t, "POST", base+path, body, headers)
		data, _ := io.ReadAll(res.Body)
		if res.StatusCode != 200 || string(data) != reply || res.Header.Get("Content-Type") != "application/json" {
			t.Fatal("response was changed", res.Status, string(data))
		}
		got := <-calls
		if got.Endpoint != strings.TrimPrefix(path, "/v1") {
			t.Fatal("wrong upstream path", got.Endpoint)
		}
		if strings.HasSuffix(path, "/compact") && !got.AgentInitiated {
			t.Fatal("compaction was not marked as agent initiated")
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(got.Body, &fields) != nil || string(fields["model"]) != "\"upstream-model\"" ||
			string(fields["input"]) != "[{\"type\":\"compaction\",\"encrypted_content\":\"keep-me\"}]" ||
			string(fields["unknown"]) != "9007199254740993" || string(fields["instructions"]) != "\"preserve\"" {
			t.Fatal("alias mapping altered compaction payload", string(got.Body))
		}
		if conversation != "" && got.ConversationID != conversation {
			t.Fatal("compaction broke conversation continuity")
		}
		conversation = got.ConversationID
	}
	for _, model := range []string{"claude-only", "not-enabled"} {
		res := send(t, "POST", base+"/responses/compact", "{\"model\":\""+model+"\"}", headers)
		if res.StatusCode != 400 {
			t.Fatal("incompatible or disabled alias accepted for compaction")
		}
	}
}

func TestStreamFlushAndProviderRevocation(t *testing.T) {
	cancelled := make(chan struct{})
	var once sync.Once
	client := &rawClient{fn: func(ctx context.Context, r provider.RawRequest) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			io.WriteString(writer, "data: first\n\n")
			<-ctx.Done()
			once.Do(func() { close(cancelled) })
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: reader}, nil
	}}
	s, base := fixture(t, client)
	res := send(t, "POST", base+"/v1/messages", `{"model":"claude-fixture"}`, map[string]string{"X-Api-Key": "fixture-key"})
	first := make(chan string, 1)
	go func() { b := make([]byte, 13); n, _ := io.ReadFull(res.Body, b); first <- string(b[:n]) }()
	select {
	case got := <-first:
		if got != "data: first\n\n" {
			t.Fatal(got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("stream was buffered")
	}
	s.Update(nil, nil)
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("revoked provider kept streaming")
	}
	if send(t, "POST", base+"/v1/messages", `{"model":"claude-fixture"}`, map[string]string{"X-Api-Key": "fixture-key"}).StatusCode != 404 {
		t.Fatal("revoked model available")
	}
}
