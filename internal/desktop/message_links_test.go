package desktop

import (
	"slices"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestAppLinkIsBoundToAnEnabledProvider(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "Doubao", "")
	if err != nil {
		t.Fatal(err)
	}
	if a.appLinkAction(id, doubao.RecoveryURL) == nil {
		t.Fatal("Doubao recovery route is unavailable")
	}
	for _, dest := range []string{
		"copilotchat://providers/doubao/open?provider=other",
		"copilotchat://providers/doubao/open#anything",
		"copilotchat://providers/doubao/open/../open",
		"copilotchat://doubao/open",
		"copilotchat://attacker.invalid/providers/doubao/open",
		"copilotchat://providers/doubao/execute",
		"app://providers/doubao/open",
	} {
		if a.appLinkAction(id, dest) != nil {
			t.Fatalf("unregistered operation accepted: %s", dest)
		}
	}
	for _, providerID := range []string{"missing", store.DefaultProviderID} {
		if a.appLinkAction(providerID, doubao.RecoveryURL) != nil {
			t.Fatal("recovery route rebound to another provider")
		}
	}
	action := a.appLinkAction(id, doubao.RecoveryURL)
	a.provider(id).Disabled = true
	if a.appLinkAction(id, doubao.RecoveryURL) != nil {
		t.Fatal("disabled provider accepted a recovery action")
	}
	action()
	if a.webAPILoginID != "" || a.settingsWin != nil {
		t.Fatal("stale action opened a disabled provider")
	}
}

func TestErrorAndAnswerUseMarkdownLinks(t *testing.T) {
	a := providerTestApp(t)
	id, err := a.addProviderKind(store.ProviderKindDoubao, "Doubao", "")
	if err != nil {
		t.Fatal(err)
	}
	a.state.Conversations = []store.Conversation{{ID: "links", Mode: "native", ProviderID: id, Messages: []store.Message{{
		ID: "answer", Role: "assistant", ProviderID: id,
		Content: "[Open from answer](" + doubao.RecoveryURL + ")",
		Error:   doubao.LoginRequired().Error(),
	}}}}
	a.state.ActiveID = "links"
	tester := ui.NewTester(a.view, 1060, 720)
	if !tester.HasText("Open from answer") || !tester.HasText("Open Doubao") {
		t.Fatal("answer or error did not render its Markdown link")
	}
	if a.webAPILoginID != "" || a.settingsWin != nil {
		t.Fatal("rendering a link executed an application action")
	}
	a.provider(id).Disabled = true
	for _, label := range []string{"Open from answer", "Open Doubao"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if a.webAPILoginID != "" || a.settingsWin != nil || len(tester.OpenedURLs()) != 0 {
		t.Fatal("disabled provider link executed or escaped to the system opener")
	}
}

func TestMessageLinksKeepExternalURLSafety(t *testing.T) {
	a := providerTestApp(t)
	const source = "[Web](https://example.com) [Mail](mailto:test@example.com) [Script](javascript:alert(1)) [File](file:///secret) [Relative](secret.txt) [App](copilotchat://providers/doubao/execute)"
	tester := ui.NewTester(func(c *ui.Context) {
		a.theme(c)
		markdown.View(c, source, a.messageMarkdownOptions(c, c.Root(), "missing"))
	}, 1000, 200)
	for _, label := range []string{"Web", "Mail", "Script", "File", "Relative", "App"} {
		if err := tester.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	if !slices.Equal(tester.OpenedURLs(), []string{"https://example.com", "mailto:test@example.com"}) {
		t.Fatalf("unsafe destinations reached system opener: %v", tester.OpenedURLs())
	}
}
