package desktop

import (
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/copilot"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestComposerPreservesDraftWhenDisconnected(t *testing.T) {
	a := &app{state: store.DefaultState()}
	tt := ui.NewTester(a.view, 1060, 720)
	if err := tt.Click("Message"); err != nil {
		t.Fatal(err)
	}
	tt.Type("请解释原生界面")
	tt.Key(ui.Shift, ui.KeyEnter)
	tt.Type("第二行")
	if a.draft != "请解释原生界面\n第二行" {
		t.Fatalf("draft = %q", a.draft)
	}
	tt.Key(0, ui.KeyEnter)
	if a.draft == "" || len(a.state.Conversations) != 0 || !tt.HasText("Connect GitHub Copilot in Settings") {
		t.Fatalf("send discarded draft or failed silently: %q, %v", a.draft, tt.Texts())
	}
}

func TestConversationActionsAndPersistence(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{storage: s, state: store.DefaultState()}
	a.newChat()
	a.current().Title = "A saved conversation"
	a.current().Messages = []store.Message{{ID: "u1", Role: "user", Content: "Native UI"}}
	a.save()
	tt := ui.NewTester(a.view, 1060, 720)
	if err := tt.RightClick("A saved conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Favorite"); err != nil {
		t.Fatal(err)
	}
	if !a.current().Favorite || !tt.HasText("Favorites") {
		t.Fatal("favorite was not applied")
	}
	if err := tt.RightClick("A saved conversation"); err != nil {
		t.Fatal(err)
	}
	if err := tt.ChooseMenuItem("Rename"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("Conversation name"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("Renamed conversation")
	if err := tt.Click("Save name"); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Conversations[0].Title != "Renamed conversation" || !loaded.Conversations[0].Favorite {
		t.Fatalf("saved = %+v", loaded.Conversations[0])
	}
	if err := tt.Click("New Chat"); err != nil {
		t.Fatal(err)
	}
	if len(a.state.Conversations) != 2 || len(a.current().Messages) != 0 {
		t.Fatal("new conversation did not open")
	}
}

func TestToolDenialClosesPendingApproval(t *testing.T) {
	reply := make(chan bool, 1)
	a := &app{state: store.DefaultState(), approval: &toolApproval{Server: "Example", Name: "write_file", Arguments: `{"path":"note.txt"}`, Reply: reply}, approvalOpen: true}
	tt := ui.NewTester(a.view, 1060, 720)
	if err := tt.Click("Deny"); err != nil {
		t.Fatal(err)
	}
	select {
	case allow := <-reply:
		if allow {
			t.Fatal("denial approved tool")
		}
	default:
		t.Fatal("worker left waiting")
	}
	if a.approval != nil || a.approvalOpen {
		t.Fatal("approval remained open")
	}
}

func TestMainLayoutAndSnapshots(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.Settings.Theme = "light"
	tt := ui.NewTester(a.view, 1060, 720)
	assertInWindow(t, tt, "Message", 1060, 720)
	assertInWindow(t, tt, "Add attachment", 1060, 720)
	writeViewScreenshot(t, tt, "chat-empty.png")
	now := time.Now()
	runtime := a.providerState(store.DefaultProviderID)
	runtime.client = copilot.NewClient("fixture-not-a-credential")
	runtime.models = []copilot.Model{{ID: "gpt-4.1", Name: "GPT-4.1"}, {ID: "claude-sonnet", Name: "Claude Sonnet"}}
	a.state.Settings.Model = "gpt-4.1"
	a.state.ActiveID = "example"
	a.state.Conversations = []store.Conversation{{ID: "example", Title: "A native space for ideas", Model: "gpt-4.1", CreatedAt: now, UpdatedAt: now, Messages: []store.Message{
		{ID: "question", Role: "user", Content: "How is this native chat app built?"},
		{ID: "answer", Role: "assistant", Content: "The interface is written in **Go**, using MyGo's native UI toolkit.\n\n- **Native rendering** — text, controls, and Markdown are drawn by the GPU.\n- **GitHub Copilot** — sign in once and choose a model from your account.\n- **Local conversations** — chats stay on this device and reopen where you left them.\n\n```go\nmygo.NewWindow(mygo.WindowOptions{\n    Title:   \"Copilot Chat\",\n    Content: ui.View(app.view),\n})\n```\n\nAttach a file below to give the conversation more context."},
	}}}
	tt.Frame()
	writeViewScreenshot(t, tt, "chat-light.png")
	a.state.Settings.Theme = "dark"
	tt.Frame()
	writeViewScreenshot(t, tt, "chat-dark.png")
	tt.SetSize(720, 480)
	assertInWindow(t, tt, "Message", 720, 480)
	assertInWindow(t, tt, "Send message", 720, 480)
	if strings.Contains(strings.Join(tt.Texts(), "|"), "Command Center") {
		t.Fatal("removed composer action exists")
	}
}

// A wide window must preserve the reading column instead of stretching turns
// and the composer across the display. Small-window-only checks missed this.
func TestChatReadingColumnAcrossWindowSizes(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.ActiveID = "layout"
	a.state.Conversations = []store.Conversation{{ID: "layout", Title: "问候与对话开启", Messages: []store.Message{
		{ID: "user", Role: "user", Content: "hello?"},
		{ID: "assistant", Role: "assistant", Reasoning: "Check the conversation context before replying.", Content: "A plain assistant reply"},
	}}}
	tt := ui.NewTester(a.view, 1920, 1032)
	tt.SetTitleBar(ui.TitleBar{Height: 50, Right: 138})
	for _, width := range []int{1920, 1280, 1060, 720} {
		tt.SetSize(width, 1032)
		composer, ok := tt.Find("Message composer")
		if !ok {
			t.Fatal("composer missing")
		}
		wantWidth := min(float32(width)-240, float32(1024)) - 40
		if math.Abs(float64(composer.W-wantWidth)) > 2 {
			t.Errorf("window %d: composer width %.1f, want %.1f", width, composer.W, wantWidth)
		}
		wantCenter := (float32(width) + 240) / 2
		if math.Abs(float64(composer.X+composer.W/2-wantCenter)) > 2 {
			t.Errorf("window %d: composer is not centered: %+v", width, composer)
		}
		user, _ := tt.Find("hello?")
		if math.Abs(float64(user.X+user.W-composer.X-composer.W)) > 2 {
			t.Errorf("window %d: user bubble does not align with composer: %+v / %+v", width, user, composer)
		}
		assertInWindow(t, tt, "Message", float32(width), 1032)
		assertInWindow(t, tt, "Add attachment", float32(width), 1032)
	}
	tt.SetSize(1920, 1032)
	writeViewScreenshot(t, tt, "chat-fullscreen.png")
}

func TestChatFontSizeChangesAssistantMarkdown(t *testing.T) {
	a := &app{state: store.DefaultState()}
	a.state.ActiveID = "font"
	a.state.Conversations = []store.Conversation{{ID: "font", Messages: []store.Message{
		{ID: "assistant", Role: "assistant", Content: "A plain assistant reply"},
	}}}
	a.state.Settings.FontSize = 16
	tt := ui.NewTester(a.view, 1060, 720)
	small, ok := tt.Find("A plain assistant reply")
	if !ok {
		t.Fatal("assistant text missing")
	}
	a.state.Settings.FontSize = 24
	tt.Frame()
	large, ok := tt.Find("A plain assistant reply")
	if !ok || large.H < small.H*1.3 {
		t.Fatalf("assistant Markdown did not follow font size: small=%+v large=%+v", small, large)
	}
}

func assertInWindow(t *testing.T, tt *ui.Tester, label string, w, h float32) {
	t.Helper()
	r, ok := tt.Find(label)
	if !ok {
		t.Fatalf("missing %s", label)
	}
	if r.W <= 0 || r.H <= 0 || r.X < 0 || r.Y < 0 || r.X+r.W > w+.5 || r.Y+r.H > h+.5 {
		t.Fatalf("%s outside %vx%v: %+v", label, w, h, r)
	}
}

func writeViewScreenshot(t *testing.T, tt *ui.Tester, name string) {
	t.Helper()
	dir := os.Getenv("COPILOT_CHAT_SCREENSHOTS")
	if dir == "" {
		return
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = png.Encode(f, tt.Image()); err != nil {
		t.Fatal(err)
	}
}
