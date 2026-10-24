package desktop

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func editingApp() *app {
	a := featureApp()
	a.state.ActiveID = "edit"
	a.state.Conversations = []store.Conversation{{ID: "edit", ProviderID: store.DefaultProviderID, Model: "text", Messages: []store.Message{
		{ID: "u1", Role: "user", Content: "Original question", CreatedAt: time.Date(2026, 10, 15, 12, 30, 0, 0, time.Local)},
		{ID: "a1", Role: "assistant", Content: "Original answer", CreatedAt: time.Date(2026, 10, 15, 12, 31, 0, 0, time.Local)},
		{ID: "u2", Role: "user", Content: "Follow up"},
		{ID: "a2", Role: "assistant", Content: "Later answer"},
	}}}
	return a
}

func TestRegenerationBuildsValidatedHistoryWithoutMutatingChat(t *testing.T) {
	a := editingApp()
	before, _ := json.Marshal(a.current())
	edit := "Revised question"
	for _, id := range []string{"u1", "a1"} {
		messages, err := a.regenerationHistory(a.current(), id, &edit)
		if err != nil || len(messages) != 1 || messages[0].Content != edit {
			t.Fatalf("wrong regeneration history: %#v, %v", messages, err)
		}
	}
	after, _ := json.Marshal(a.current())
	if !bytes.Equal(before, after) {
		t.Fatal("preflight mutated original conversation")
	}
	a.provider(store.DefaultProviderID).DisabledModels = []string{"text"}
	a.editMessageID, a.editText = "u1", edit
	if a.saveMessageEdit() || a.streaming {
		t.Fatal("hidden model accepted")
	}
	after, _ = json.Marshal(a.current())
	if !bytes.Equal(before, after) || a.editText != edit || a.editMessageID != "u1" {
		t.Fatal("failed edit discarded conversation or draft")
	}
}

func TestRegenerationOnlyChecksRetainedAttachments(t *testing.T) {
	a := editingApp()
	a.current().Messages[2].Attachments = []store.Attachment{{MIME: "image/png"}}
	if _, err := a.regenerationHistory(a.current(), "u1", nil); err != nil {
		t.Fatalf("discarded future attachment prevented regeneration: %v", err)
	}
	if _, err := a.regenerationHistory(a.current(), "u2", nil); err == nil {
		t.Fatal("retained incompatible image accepted")
	}
}

func TestEditingAssistantReplacesProviderHistoryWithoutRegenerating(t *testing.T) {
	a := editingApp()
	m := &a.current().Messages[1]
	m.ProviderMessages = []json.RawMessage{wire(t, provider.Message{Role: "assistant", Content: "stale wire answer", ReasoningOpaque: "signature"})}
	m.Reasoning, m.Error = "old reasoning", "old error"
	m.Stats = &store.MessageStats{OutputTokens: 10}
	m.ToolActivity = []string{"previous tool"}
	stamp := m.CreatedAt
	tt := ui.NewTester(a.view, 1060, 720)
	body, _ := tt.Find("Original answer")
	tt.Move(body.X+5, body.Y+5)
	if err := tt.Click("Edit response"); err != nil {
		t.Fatal(err)
	}
	a.editText = "Manually corrected answer"
	tt.Frame()
	if err := tt.Click("Save changes"); err != nil {
		t.Fatal(err)
	}
	if a.streaming || len(a.current().Messages) != 4 || m.Content != "Manually corrected answer" {
		t.Fatal("assistant edit regenerated or lost text")
	}
	if m.Stats != nil || m.ProviderMessages != nil || m.Reasoning != "" || m.Error != "" || m.ToolActivity != nil || !m.CreatedAt.Equal(stamp) {
		t.Fatal("edited response kept stale provider data or lost timestamp")
	}
	h := history(a.current())
	if len(h) != 4 || h[1].Content != "Manually corrected answer" {
		t.Fatalf("next request replays old response: %#v", h)
	}
}

const testPreviewPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII="

func TestImagePreviewKeepsOriginalBytesAndUsesDetectedSafeExtension(t *testing.T) {
	f := store.Attachment{Name: "../../run.exe", MIME: "image/png", Data: testPreviewPNG}
	dir := t.TempDir()
	path, err := writeAttachmentPreview(dir, f)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != filepath.Join(dir, "image-previews") || filepath.Ext(path) != ".png" {
		t.Fatalf("unsafe preview path: %s", path)
	}
	got, err := os.ReadFile(path)
	want, _ := base64.StdEncoding.DecodeString(f.Data)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatal("preview did not preserve original image")
	}
	f.Data = base64.StdEncoding.EncodeToString([]byte("not an image"))
	if _, err = writeAttachmentPreview(dir, f); err == nil {
		t.Fatal("invalid image was materialized")
	}
}

func TestRemovingDraftImagePreservesSentAttachment(t *testing.T) {
	a := editingApp()
	f := store.Attachment{Name: "screenshot.png", MIME: "image/png", Data: testPreviewPNG, Size: 68}
	a.attachments = []store.Attachment{f}
	a.current().Messages[0].Attachments = []store.Attachment{f}
	tt := ui.NewTester(a.view, 1060, 720)
	if _, ok := tt.Find("Open image screenshot.png"); !ok {
		t.Fatal("sent image cannot be opened")
	}
	if !tt.HasText("screenshot.png") {
		t.Fatal("composer filename missing")
	}
	if err := tt.Click("Remove screenshot.png"); err != nil {
		t.Fatal(err)
	}
	if len(a.attachments) != 0 || !reflect.DeepEqual(a.current().Messages[0].Attachments, []store.Attachment{f}) {
		t.Fatal("removing draft image changed sent attachment")
	}
	if !strings.Contains(strings.Join(tt.Texts(), " "), "12:31") {
		t.Fatal("assistant timestamp missing")
	}
}
