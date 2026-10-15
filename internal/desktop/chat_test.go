package desktop

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/fondoger/copilotchat/internal/copilot"
	"github.com/fondoger/copilotchat/internal/mcp"
	"github.com/fondoger/copilotchat/internal/store"
)

func wire(t *testing.T, message copilot.Message) json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestHistoryPreservesStoppedToolWorkAndPartialText(t *testing.T) {
	chat := store.Conversation{Messages: []store.Message{
		{Role: "user", Content: "look up the result"},
		{Role: "assistant", Error: "Response stopped.", ProviderMessages: []json.RawMessage{
			wire(t, copilot.Message{Role: "assistant", Content: "Looking", ToolCalls: []copilot.ToolCall{
				{ID: "done", Name: "lookup", Arguments: `{}`},
				{ID: "pending", Name: "write", Arguments: `{}`},
			}}),
			wire(t, copilot.Message{Role: "tool", ToolCallID: "done", Content: "already completed"}),
		}},
		{Role: "user", Content: "what happened?"},
		{Role: "assistant", Content: "partial explanation", Error: "network disconnected"},
	}}
	messages := history(&chat)
	if len(messages) != 6 {
		t.Fatalf("lost prior tool work: %#v", messages)
	}
	if messages[2].Content != "already completed" || messages[3].ToolCallID != "pending" || !strings.Contains(messages[3].Content, "interrupted") {
		t.Fatalf("tool transcript not repaired: %#v", messages)
	}
	if messages[5].Content != "partial explanation" {
		t.Fatal("visible partial assistant text was discarded")
	}
	if len(chat.Messages[1].ProviderMessages) != 2 {
		t.Fatal("building history mutated saved messages")
	}
}

func TestHistoryRejectsOrphanToolsAndFallsBackFromMalformedMetadata(t *testing.T) {
	chat := store.Conversation{Messages: []store.Message{
		{Role: "assistant", Content: "visible fallback", ProviderMessages: []json.RawMessage{json.RawMessage(`{bad}`)}},
		{Role: "tool", Content: "orphan"},
		{Role: "assistant", Error: "stopped"},
		{Role: "user", Content: "question", Attachments: []store.Attachment{{Name: "notes.txt", MIME: "text/plain", Data: "ZmlsZQ=="}}},
	}}
	messages := history(&chat)
	if len(messages) != 2 || messages[0].Content != "visible fallback" || len(messages[1].Attachments) != 1 {
		t.Fatalf("incorrect recovery history: %#v", messages)
	}
}

func TestRecordReplyPersistsToolResultBeforeNextModelCall(t *testing.T) {
	disk, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{storage: disk, state: store.DefaultState()}
	a.state.Conversations = []store.Conversation{{ID: "chat", Messages: []store.Message{{ID: "reply", Role: "assistant"}}}}
	transcript := []json.RawMessage{
		wire(t, copilot.Message{Role: "assistant", ToolCalls: []copilot.ToolCall{{ID: "call", Name: "tool", Arguments: `{}`}}}),
		wire(t, copilot.Message{Role: "tool", ToolCallID: "call", Content: "saved result"}),
	}
	a.recordReply("chat", "reply", "working", transcript)
	saved, err := disk.Load()
	if err != nil {
		t.Fatal(err)
	}
	messages := history(&saved.Conversations[0])
	if len(messages) != 2 || messages[1].Content != "saved result" {
		t.Fatalf("tool result was not durable: %#v", messages)
	}
}

func TestCancellationInvalidatesLoginModelsAndTitleJobs(t *testing.T) {
	loginCtx, loginCancel := context.WithCancel(context.Background())
	modelCtx, modelCancel := context.WithCancel(context.Background())
	titleCtx, titleCancel := context.WithCancel(context.Background())
	runtime := &providerRuntime{authBusy: true, authEpoch: 1, authCancel: loginCancel, modelsBusy: true, modelsCancel: modelCancel}
	a := &app{state: store.DefaultState(), providers: map[string]*providerRuntime{store.DefaultProviderID: runtime}, titleJobs: map[string]*titleJob{"chat": {cancel: titleCancel}}}
	if !a.currentProviderLogin(store.DefaultProviderID, 1, loginCtx) {
		t.Fatal("active login was rejected")
	}
	a.cancelLogin()
	a.cancelModelRefresh()
	a.cancelTitles()
	if loginCtx.Err() == nil || modelCtx.Err() == nil || titleCtx.Err() == nil || runtime.authBusy || runtime.modelsBusy || len(a.titleJobs) != 0 {
		t.Fatal("canceling left active background work")
	}
	runtime.authBusy = true // A later login has started before the old callback arrives.
	if a.currentProviderLogin(store.DefaultProviderID, 1, context.Background()) {
		t.Fatal("stale login callback can overwrite a new login")
	}
}

func TestDraftNavigationAndNewChatKeepUnsentContent(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.ActiveID = "first"
	a.state.Conversations = []store.Conversation{{ID: "first"}, {ID: "second"}}
	a.draft = "unsent first message"
	a.attachments = []store.Attachment{{Name: "notes.txt", Data: "ZmlsZQ=="}}
	a.selectChat("second")
	if a.draft != "" || len(a.attachments) != 0 {
		t.Fatal("newly selected chat inherited another chat's draft")
	}
	a.draft = "second draft"
	a.selectChat("first")
	if a.draft != "unsent first message" || len(a.attachments) != 1 || a.attachments[0].Name != "notes.txt" {
		t.Fatal("draft navigation discarded text or attachments")
	}
	a.newChat()
	if a.state.ActiveID == "first" || a.draft != "" || len(a.attachments) != 0 {
		t.Fatal("new chat did not create an empty composer")
	}
	a.selectChat("first")
	if a.draft != "unsent first message" {
		t.Fatal("New Chat discarded the old draft")
	}
	a.attachments[0].Name = "edited.txt"
	if a.drafts["first"].Attachments[0].Name != "notes.txt" {
		t.Fatal("draft attachment snapshots share mutable backing arrays")
	}
}

func TestEnabledServersSnapshotDoesNotShareConfig(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.Settings.MCPServers = []mcp.Config{
		{ID: "on", Enabled: true, Args: []string{"old"}, Env: map[string]string{"VALUE": "old"}},
		{ID: "off", Enabled: false},
	}
	snapshot := a.enabledServers()
	a.state.Settings.MCPServers[0].Args[0] = "new"
	a.state.Settings.MCPServers[0].Env["VALUE"] = "new"
	if len(snapshot) != 1 || snapshot[0].Args[0] != "old" || snapshot[0].Env["VALUE"] != "old" {
		t.Fatalf("worker configuration changed with UI: %#v", snapshot)
	}
}

func TestToolAliasAndOutputTruncation(t *testing.T) {
	if toolAlias("server", "lookup") != toolAlias("server", "lookup") || toolAlias("server", "lookup") == toolAlias("other", "lookup") {
		t.Fatal("tool aliases are unstable or collide across servers")
	}
	text := "abc你好world"
	truncated := truncateToolOutput(text, 7)
	if !utf8.ValidString(truncated) || !strings.HasPrefix(truncated, "abc你\n") {
		t.Fatalf("tool output split a UTF-8 character: %q", truncated)
	}
}

func TestAttachmentReadsAndLimits(t *testing.T) {
	dir := t.TempDir()
	for _, test := range []struct {
		name string
		data []byte
		mime string
	}{
		{"notes.txt", []byte("Hello 你好"), "text/plain"},
		{"paper.pdf", []byte("%PDF-1.7\nexample"), "application/pdf"},
		{"image.png", []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, "image/png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(dir, test.name)
			if err := os.WriteFile(path, test.data, 0600); err != nil {
				t.Fatal(err)
			}
			file, err := readAttachment(path)
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := base64.StdEncoding.DecodeString(file.Data)
			if err != nil || file.Name != test.name || file.MIME != test.mime || !reflect.DeepEqual(decoded, test.data) {
				t.Fatalf("attachment changed: %#v, %v", file, err)
			}
		})
	}
	if _, err := readAttachment(dir); err == nil {
		t.Fatal("directory accepted as attachment")
	}
	path := filepath.Join(dir, "large.txt")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxAttachmentBytes + 1); err != nil {
		t.Fatal(err)
	}
	f.Close()
	if _, err := readAttachment(path); err == nil {
		t.Fatal("oversized attachment accepted")
	}
}

func TestLocalTitleUsesUnicodeAndFileFallback(t *testing.T) {
	if title := localTitle(" \n hello   world ", nil); title != "hello world" {
		t.Fatalf("title whitespace not normalized: %q", title)
	}
	if title := localTitle(strings.Repeat("中", 60), nil); !utf8.ValidString(title) || utf8.RuneCountInString(title) != 49 {
		t.Fatalf("Unicode title truncated incorrectly: %q", title)
	}
	if title := localTitle("", []store.Attachment{{Name: "notes.txt"}}); title != "notes.txt" {
		t.Fatalf("missing attachment title: %q", title)
	}
}
