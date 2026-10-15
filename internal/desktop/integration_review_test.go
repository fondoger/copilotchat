package desktop

import (
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/copilot"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestLegacyReasoningCannotCrossProviderOrModel(t *testing.T) {
	for _, test := range []struct {
		name, provider, model string
		keep                  bool
	}{
		{"known-same-origin", "work", "model", true},
		{"legacy-missing-origin", "", "", false},
		{"missing-model", "work", "", false},
		{"same-model-other-provider", "personal", "model", false},
		{"same-provider-other-model", "work", "other", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			chat := store.Conversation{ProviderID: "work", Model: "model", Messages: []store.Message{{ProviderID: test.provider, Model: test.model, Role: "assistant", ProviderMessages: []json.RawMessage{
				wire(t, copilot.Message{Role: "assistant", Content: "Answer", ReasoningOpaque: "private", ProviderData: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private"}`)}}),
			}}}}
			messages := history(&chat)
			if len(messages) != 1 || messages[0].Content != "Answer" {
				t.Fatalf("answer lost: %#v", messages)
			}
			if (messages[0].ReasoningOpaque != "") != test.keep || (len(messages[0].ProviderData) != 0) != test.keep {
				t.Fatalf("incorrect reasoning origin check: %#v", messages[0])
			}
		})
	}
}

func TestRetryPreflightPreservesPreviousAnswer(t *testing.T) {
	for _, kind := range []string{"hidden", "unavailable", "disconnected", "attachment", "effort"} {
		t.Run(kind, func(t *testing.T) {
			a := providerTestApp(t)
			a.state.ActiveID = "chat"
			a.state.Conversations = []store.Conversation{{ID: "chat", ProviderID: "work", Model: "same", Messages: []store.Message{
				{Role: "user", Content: "Question"},
				{Role: "assistant", Content: "Keep this answer"},
			}}}
			switch kind {
			case "hidden":
				a.provider("work").ModelFilter = "whitelist"
			case "unavailable":
				a.current().Model = "personal-only"
			case "disconnected":
				a.providers["work"].client = nil
			case "attachment":
				a.current().Messages[0].Attachments = []store.Attachment{{Name: "photo.png", MIME: "image/png"}}
			case "effort":
				a.current().ReasoningEffort = "unsupported"
			}
			a.retryLast()
			if a.streaming || len(a.current().Messages) != 2 || a.current().Messages[1].Content != "Keep this answer" || a.status == "" {
				t.Fatalf("retry discarded a reply before rejecting invalid %s: %#v, %s", kind, a.current().Messages, a.status)
			}
		})
	}
}

func TestSendPreflightPreservesDraftAfterSwitchingToTextModel(t *testing.T) {
	a := featureApp()
	a.state.ActiveID = "chat"
	a.state.Conversations = []store.Conversation{{ID: "chat", ProviderID: store.DefaultProviderID, Model: "text", Messages: []store.Message{
		{Role: "user", Attachments: []store.Attachment{{Name: "photo.png", MIME: "image/png"}}},
		{Role: "assistant", Content: "An earlier answer"},
	}}}
	a.draft = "Next question"
	if a.send() || a.streaming || len(a.current().Messages) != 2 || a.draft != "Next question" || !strings.Contains(a.status, "previous message") {
		t.Fatalf("incompatible history consumed draft: %#v, %q, %s", a.current(), a.draft, a.status)
	}
}

func TestThinkingOptionsStayWithinProtocolBudgets(t *testing.T) {
	for _, test := range []struct {
		name  string
		model copilot.Model
		want  []string
	}{
		{"responses", copilot.Model{Endpoint: "/responses", MaxThinkingBudget: 8192}, []string{"default"}},
		{"adaptive-without-efforts", copilot.Model{Endpoint: "/v1/messages", AdaptiveThinking: true, MaxThinkingBudget: 8192}, []string{"default"}},
		{"adaptive", copilot.Model{Endpoint: "/v1/messages", AdaptiveThinking: true, ThinkingEfforts: []string{"medium", "max"}}, []string{"default", "medium", "max"}},
		{"anthropic-bounds", copilot.Model{Endpoint: "/v1/messages", MinThinkingBudget: 128, MaxThinkingBudget: 4096, MaxOutputTokens: 4096}, []string{"default", "budget:1024", "budget:2048", "budget:4095"}},
		{"chat-bounds", copilot.Model{Endpoint: "/chat/completions", MinThinkingBudget: 128, MaxThinkingBudget: 512}, []string{"default", "budget:128", "budget:512"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var got []string
			for _, option := range thinkingOptions(test.model) {
				got = append(got, option.Value)
				if strings.HasPrefix(option.Value, "budget:") {
					budget, _ := strconv.Atoi(strings.TrimPrefix(option.Value, "budget:"))
					if err := thinkingSettingsError(test.model, "", budget); err != nil {
						t.Errorf("offered an invalid budget: %v", err)
					}
				}
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("incorrect thinking choices: %v, want %v", got, test.want)
			}
		})
	}
}

func TestHistoricalChatThinkingDoesNotChangeUnrelatedDefaults(t *testing.T) {
	a := providerTestApp(t)
	a.state.Settings.ActiveProviderID, a.state.Settings.Model = store.DefaultProviderID, "personal-only"
	a.state.Settings.ReasoningEffort = "default-model-effort"
	a.state.ActiveID = "work-chat"
	a.state.Conversations = []store.Conversation{{ID: "work-chat", ProviderID: "work", Model: "same"}}
	a.setReasoningSettings("high", 0)
	if a.current().ReasoningEffort != "high" || a.state.Settings.ReasoningEffort != "default-model-effort" {
		t.Fatal("historical chat thinking overwrote an unrelated provider's defaults")
	}
}

func TestStaleThinkingChoiceCanBeClearedAfterCatalogChange(t *testing.T) {
	a := featureApp()
	a.state.Settings.ReasoningEffort = "old-effort"
	tt := ui.NewTester(a.view, 1060, 720)
	if err := tt.Click("Thinking effort"); err != nil {
		t.Fatalf("stale thinking selection has no reset control: %v", err)
	}
	if err := tt.Click("Default"); err != nil {
		t.Fatal(err)
	}
	if a.state.Settings.ReasoningEffort != "" || a.state.Settings.ThinkingBudgetTokens != 0 {
		t.Fatal("could not clear obsolete thinking selection")
	}
}
