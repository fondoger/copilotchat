package desktop

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"image"
	"image/draw"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

func featureApp() *app {
	a := &app{state: store.DefaultState()}
	a.state.Settings.Theme = "light"
	a.state.Settings.Model = "text"
	r := a.providerState(store.DefaultProviderID)
	r.client = github.NewClient("test-fixture")
	r.models = []provider.Model{
		{ID: "text", Name: "Text model", VisionKnown: true},
		{ID: "vision", Name: "Vision model", Vision: true, VisionKnown: true, Reasoning: true, ThinkingEfforts: []string{"low", "high"}},
	}
	return a
}

func TestMessageHoverShowsActionsAndCopyWorks(t *testing.T) {
	a := featureApp()
	a.state.ActiveID = "hover"
	a.state.Conversations = []store.Conversation{{ID: "hover", ProviderID: store.DefaultProviderID, Model: "text", Messages: []store.Message{
		{ID: "u", Role: "user", Content: "A user message"},
		{ID: "a", Role: "assistant", Content: "An assistant reply"},
	}}}
	tt := ui.NewTester(a.view, 1060, 720)
	r, ok := tt.Find("Copy response")
	if !ok {
		t.Fatal("copy action missing")
	}
	region := image.Rect(int(r.X), int(r.Y), int(r.X+r.W), int(r.Y+r.H))
	crop := func(src image.Image) []byte {
		result := image.NewRGBA(image.Rect(0, 0, region.Dx(), region.Dy()))
		draw.Draw(result, result.Bounds(), src, region.Min, draw.Src)
		return result.Pix
	}
	before := crop(tt.Image())
	body, _ := tt.Find("An assistant reply")
	tt.Move(body.X+10, body.Y+5)
	after := crop(tt.Image())
	if bytes.Equal(before, after) {
		t.Fatal("hovering reply text did not reveal its action buttons")
	}
	if err := tt.Click("Copy response"); err != nil {
		t.Fatal(err)
	}
	if tt.Clipboard() != "An assistant reply" {
		t.Fatal("reply copy did not copy the response")
	}
	if err := tt.Click("Response details"); err != nil {
		t.Fatal(err)
	}
	if !tt.HasText("Cached input tokens") || !tt.HasText("Token usage was not reported for this response.") {
		t.Fatalf("missing stats or unknown-usage state: %v", tt.Texts())
	}
}

func TestImagePasteChecksCurrentModelAndKeepsDraft(t *testing.T) {
	a := featureApp()
	a.draft = "Keep my draft"
	tt := ui.NewTester(a.view, 1060, 720)
	if err := tt.Click("Message"); err != nil {
		t.Fatal(err)
	}
	png, _ := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	tt.SetClipboardData(transfer.New(transfer.NewItem(transfer.Bytes(transfer.PNG, png))))
	tt.Key(ui.Cmd, ui.KeyV)
	if len(a.attachments) != 0 || !strings.Contains(a.status, "does not support image input") || a.draft != "Keep my draft" {
		t.Fatalf("unsupported image paste changed draft: %q, %q, %d", a.draft, a.status, len(a.attachments))
	}
	a.chooseModel(store.DefaultProviderID, "vision")
	tt.Frame()
	if err := tt.Click("Message"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyV)
	if len(a.attachments) != 1 || a.attachments[0].MIME != "image/png" || a.draft != "Keep my draft" {
		t.Fatal("vision model did not accept the pasted image while preserving text")
	}
	if err := tt.Click("Thinking effort"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("high"); err != nil {
		t.Fatal(err)
	}
	if a.state.Settings.ReasoningEffort != "high" {
		t.Fatal("thinking effort was not persisted")
	}
	a.chooseModel(store.DefaultProviderID, "text")
	tt.Frame()
	if a.state.Settings.ReasoningEffort != "" {
		t.Fatal("non-reasoning model inherited an effort")
	}
	if a.send() || len(a.attachments) != 1 || a.draft != "Keep my draft" {
		t.Fatal("send did not reject incompatible attachments without discarding the draft")
	}
}

func TestReplyStatsAccumulateAndPreserveUnknownCacheUsage(t *testing.T) {
	s := &store.MessageStats{}
	accumulateReplyStats(s, provider.Completion{
		Usage:   provider.Usage{InputTokens: 442, OutputTokens: 4900, CachedInputTokens: 100, InputAvailable: true, OutputAvailable: true, CachedInputAvailable: true},
		Metrics: provider.Metrics{TimeToFirstToken: 7300 * time.Millisecond, Duration: 57700 * time.Millisecond},
	}, true, 0)
	s.DurationMS = 57700
	rows := replyStatsRows(s)
	if rows[0][1] != "442" || rows[1][1] != "100" || rows[3][1] != "7.3s" || rows[5][1] != "84.9 T/s" {
		t.Fatalf("stats incorrect: %v", rows)
	}
	accumulateReplyStats(s, provider.Completion{Usage: provider.Usage{InputTokens: 10, OutputTokens: 5, InputAvailable: true, OutputAvailable: true}}, false, time.Minute)
	rows = replyStatsRows(s)
	if rows[0][1] != "452" || rows[1][1] != "—" || rows[2][1] != "4905" {
		t.Fatalf("unknown cache usage was reported as a measured count: %v", rows)
	}
}

func TestArtifactSourceCannotEscapeSandbox(t *testing.T) {
	source := `</iframe><script>window.parent.mygo.call("danger")</script><img src="https://remote.invalid/data">`
	doc := artifactDocument(source)
	if strings.Contains(doc, source) || strings.Count(doc, "<iframe") != 1 || strings.Contains(doc, "allow-same-origin") {
		t.Fatal("model content escaped the opaque iframe")
	}
	for _, rule := range []string{`sandbox="allow-scripts"`, "connect-src 'none'", "default-src 'none'", "form-action 'none'"} {
		if !strings.Contains(doc, rule) {
			t.Fatalf("missing sandbox constraint %s", rule)
		}
	}
}

func TestHTMLCodeBlockOffersInlinePreviewAndSource(t *testing.T) {
	a := featureApp()
	a.state.ActiveID = "html"
	a.state.Conversations = []store.Conversation{{ID: "html", ProviderID: store.DefaultProviderID, Messages: []store.Message{{ID: "answer", Role: "assistant", Content: "```html\n<h1>Hello</h1>\n```"}}}}
	tt := ui.NewTester(a.view, 1060, 720)
	body, ok := tt.Find("<h1>Hello</h1>")
	if !ok {
		t.Fatal("HTML source snippet missing")
	}
	tt.Move(body.X+5, body.Y+5)
	if err := tt.Click("Run HTML preview"); err != nil {
		t.Fatal(err)
	}
	if a.preview == nil || !strings.Contains(a.preview.source, "<h1>Hello</h1>") {
		t.Fatal("preview did not receive the code block")
	}
	if err := tt.Click("Close preview"); err != nil {
		t.Fatal(err)
	}
	if a.preview != nil {
		t.Fatal("preview was not released")
	}
}

func TestProviderSwitchDropsOpaqueReasoningButKeepsText(t *testing.T) {
	chat := store.Conversation{ProviderID: "second", Model: "other", Messages: []store.Message{{ProviderID: "first", Model: "old", Role: "assistant", ProviderMessages: []json.RawMessage{wire(t, provider.Message{Role: "assistant", Content: "keep this", ReasoningOpaque: "signature", ProviderData: []json.RawMessage{json.RawMessage(`{"type":"reasoning","encrypted_content":"private"}`)}})}}}}
	messages := history(&chat)
	if len(messages) != 1 || messages[0].Content != "keep this" || messages[0].ReasoningOpaque != "" || len(messages[0].ProviderData) != 0 {
		t.Fatalf("wrong provider replay: %+v", messages)
	}
}
