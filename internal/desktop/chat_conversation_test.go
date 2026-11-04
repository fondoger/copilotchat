package desktop

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestConversationContinuationIsPersistedAndConsumedBeforeNextAttempt(t *testing.T) {
	a := providerTestApp(t)
	state := json.RawMessage(`{"conversation_id":"123","history_hash":"history","answer_hash":"answer"}`)
	chat := store.Conversation{ID: "chat", ProviderID: store.DefaultProviderID, Model: "model", Messages: []store.Message{
		{Role: "user", Content: "question"},
		{Role: "assistant", Content: "answer", ProviderID: store.DefaultProviderID, Model: "model", ConversationState: state},
	}}
	a.state.Conversations = []store.Conversation{chat}
	a.save()
	saved, err := a.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	chat = saved.Conversations[0]
	chat.Messages = append(chat.Messages, store.Message{Role: "user", Content: "follow up"})
	var got, want map[string]string
	if json.Unmarshal(takeConversationState(&chat), &got) != nil || json.Unmarshal(state, &want) != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("reloading lost continuation: %v", got)
	}
	a.state.Conversations[0] = chat
	a.save()
	saved, err = a.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	chat = saved.Conversations[0]
	if len(takeConversationState(&chat)) != 0 {
		t.Fatal("an interrupted attempt could reuse a consumed remote context")
	}
	// Regenerating an earlier turn cannot resurrect its old continuation point.
	chat.Messages = chat.Messages[:2]
	chat.Messages = append(chat.Messages, store.Message{Role: "user", Content: "different branch"})
	if len(takeConversationState(&chat)) != 0 {
		t.Fatal("regeneration resurrected a consumed remote context")
	}
}

func TestConversationContinuationDoesNotCrossProviderModelOrFailedReply(t *testing.T) {
	for _, scenario := range []string{"provider", "model", "failed", "not-latest"} {
		t.Run(scenario, func(t *testing.T) {
			chat := store.Conversation{ProviderID: "current", Model: "model", Messages: []store.Message{
				{Role: "assistant", ProviderID: "current", Model: "model", ConversationState: json.RawMessage(`{"id":"remote"}`)},
				{Role: "user", Content: "follow up"},
			}}
			switch scenario {
			case "provider":
				chat.ProviderID = "other"
			case "model":
				chat.Model = "other"
			case "failed":
				chat.Messages[0].Error = "Response stopped."
			case "not-latest":
				chat.Messages = append(chat.Messages, store.Message{Role: "user", Content: "another question"})
			}
			if len(takeConversationState(&chat)) != 0 || len(chat.Messages[0].ConversationState) != 0 {
				t.Fatal("incompatible continuation was reused or left available")
			}
		})
	}
}

func TestReplyReturnsContinuationSeparatelyFromAssistantHistory(t *testing.T) {
	state := json.RawMessage(`{"conversation_id":"123"}`)
	client := nativeSearchClient{complete: func(context.Context, provider.Request) (provider.Completion, error) {
		return provider.Completion{Content: "answer", ConversationState: state, ConversationTitle: "Autumn leaves"}, nil
	}}
	got, err := (&app{}).runReply(context.Background(), client, provider.Request{}, nil, "chat", "reply")
	if err != nil || string(got.ConversationState) != string(state) || got.ConversationTitle != "Autumn leaves" {
		t.Fatalf("reply lost continuation: %+v, %v", got, err)
	}
}
