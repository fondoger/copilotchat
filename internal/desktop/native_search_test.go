package desktop

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestNativeSearchTogglePersistsAndResetsOnNewChatAndModelChange(t *testing.T) {
	a := providerTestApp(t)
	a.providers[store.DefaultProviderID].models = []provider.Model{
		{ID: "gpt-search", Endpoint: "/responses"},
		{ID: "plain", Endpoint: "/chat/completions"},
	}
	a.state.Settings.Model = "gpt-search"
	tt := ui.NewTester(a.composerView, 720, 200)
	if _, ok := tt.Find("Web search"); !ok {
		t.Fatal("supported model has no search toggle")
	}
	settingsClick(t, tt, "Web search")
	chat := a.current()
	if chat == nil || !chat.NativeSearch {
		t.Fatal("clicking search before the first send did not enable it on a conversation")
	}
	saved, err := a.storage.Load()
	if err != nil || len(saved.Conversations) != 1 || !saved.Conversations[0].NativeSearch {
		t.Fatalf("search selection was not persisted: %v", err)
	}
	a.streaming = true
	tt.Frame()
	settingsClick(t, tt, "Web search")
	if !a.current().NativeSearch {
		t.Fatal("search changed during an active response")
	}
	a.streaming = false
	a.newChat()
	if a.current().NativeSearch {
		t.Fatal("New Chat inherited search from a reused blank conversation")
	}
	saved, err = a.storage.Load()
	if err != nil || saved.Conversations[0].NativeSearch {
		t.Fatal("New Chat did not persist the reset search preference")
	}
	tt.Frame()
	settingsClick(t, tt, "Web search")
	a.chooseModel(store.DefaultProviderID, "plain")
	tt.Frame()
	if a.current().NativeSearch {
		t.Fatal("model change retained search")
	}
	if _, ok := tt.Find("Web search"); ok {
		t.Fatal("unsupported model exposed search")
	}
}

func TestNativeSearchReplayAndSourcesSurviveReloadButNotModelSwitch(t *testing.T) {
	a := providerTestApp(t)
	replay := &provider.Replay{Protocol: "/v1/messages", Blocks: []json.RawMessage{
		json.RawMessage(`{"type":"server_tool_use","id":"search","name":"web_search","input":{"query":"current Go"}}`),
		json.RawMessage(`{"type":"web_search_tool_result","tool_use_id":"search","content":[{"type":"web_search_result","url":"https://go.dev/","encrypted_content":"opaque"}]}`),
		json.RawMessage(`{"type":"text","text":"Answer"}`),
	}}
	a.state.ActiveID = "chat"
	a.state.Conversations = []store.Conversation{{ID: "chat", ProviderID: store.DefaultProviderID, Model: "claude", NativeSearch: true, Messages: []store.Message{{
		ID: "reply", Role: "assistant", Content: "Answer", ProviderID: store.DefaultProviderID, Model: "claude",
		ProviderMessages: []json.RawMessage{wire(t, provider.Message{Role: "assistant", Content: "Answer", Replay: replay})},
		Citations:        []provider.Citation{{URL: "https://go.dev/", Title: "Go"}},
	}}}}
	a.save()
	saved, err := a.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	chat := &saved.Conversations[0]
	messages := history(chat)
	if len(messages) != 1 || messages[0].Replay == nil || len(chat.Messages[0].Citations) != 1 {
		t.Fatal("reloading lost the search transcript or citations")
	}
	chat.Model = "gpt-other"
	messages = history(chat)
	if len(messages) != 1 || messages[0].Replay != nil || messages[0].Content != "Answer" {
		t.Fatal("switching models leaked opaque search history or lost visible text")
	}
}

type nativeSearchClient struct {
	provider.Client
	complete func(context.Context, provider.Request) (provider.Completion, error)
}

func (c nativeSearchClient) StreamWithEvents(ctx context.Context, request provider.Request, emit func(provider.StreamEvent)) (provider.Completion, error) {
	completion, err := c.complete(ctx, request)
	if completion.Content != "" {
		emit(provider.StreamEvent{Kind: provider.EventText, Delta: completion.Content})
	}
	return completion, err
}

func TestNativeSearchPausedTurnResumesWithoutClientToolOrFabricatedUser(t *testing.T) {
	requests := 0
	client := nativeSearchClient{complete: func(_ context.Context, request provider.Request) (provider.Completion, error) {
		requests++
		if !request.NativeSearch {
			t.Fatal("search disabled during server continuation")
		}
		if requests == 1 {
			return provider.Completion{Content: "Searching", StopReason: "pause_turn", Replay: &provider.Replay{Protocol: "/v1/messages", Blocks: []json.RawMessage{json.RawMessage(`{"type":"text","text":"Searching"}`)}}}, nil
		}
		if len(request.Messages) != 2 || request.Messages[1].Role != "assistant" || request.Messages[1].Replay == nil {
			t.Fatalf("server continuation lost replay or fabricated a tool/user message: %#v", request.Messages)
		}
		return provider.Completion{Content: "Answer", StopReason: "end_turn"}, nil
	}}
	a := &app{}
	request := provider.Request{NativeSearch: true, Messages: []provider.Message{{Role: "user", Content: "Find current information"}}}
	if err := a.runReply(context.Background(), client, request, nil, "chat", "reply"); err != nil || requests != 2 {
		t.Fatalf("paused turn did not resume: requests=%d err=%v", requests, err)
	}
}

func TestNativeSearchContinuationIsBoundedAndCancellationStopsIt(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		requests := 0
		ctx, stop := context.WithCancel(context.Background())
		client := nativeSearchClient{complete: func(_ context.Context, request provider.Request) (provider.Completion, error) {
			requests++
			if cancel {
				stop()
			}
			return provider.Completion{StopReason: "pause_turn"}, nil
		}}
		err := (&app{}).runReply(ctx, client, provider.Request{NativeSearch: true}, nil, "chat", "reply")
		stop()
		if err == nil || cancel && requests != 1 || !cancel && (requests != 8 || !strings.Contains(err.Error(), "paused too many")) {
			t.Fatalf("continuation failed to stop: cancel=%v requests=%d err=%v", cancel, requests, err)
		}
	}
}

func TestSearchCitationLinksRejectUnsafeSchemesAndDeduplicate(t *testing.T) {
	citations := appendCitations(nil, []provider.Citation{
		{URL: "https://go.dev/", Title: "Go"}, {URL: "javascript:alert(1)"}, {URL: "file:///secret"}, {URL: "https://go.dev/", Title: "Repeated"},
	})
	if len(citations) != 1 || citations[0].Title != "Go" {
		t.Fatalf("unsafe or duplicate source exposed: %#v", citations)
	}
}
