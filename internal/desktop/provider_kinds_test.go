package desktop

import (
	"image/png"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

func TestProviderPickerAddsMicrosoftWithoutChangingGitHub(t *testing.T) {
	a := providerTestApp(t)
	a.settingsPage = "Providers"
	tester := ui.NewTester(a.settingsView, 900, 800)
	settingsClick(t, tester, "Add provider")
	if len(a.state.Settings.Providers) != 2 {
		t.Fatal("opening the provider picker created a provider")
	}
	for _, label := range []string{"Add GitHub Copilot", "Add Microsoft 365 Copilot", "Microsoft 365 Copilot icon", "GitHub Copilot icon"} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("provider picker missing %q", label)
		}
	}
	saveProviderScreenshot(t, tester, "provider-type-picker.png")
	settingsClick(t, tester, "Add Microsoft 365 Copilot")
	p := a.provider(a.providerSettingsID)
	if p == nil || p.Kind != store.ProviderKindM365 || p.Host != store.M365ProviderHost || len(a.state.Settings.Providers) != 3 {
		t.Fatalf("wrong provider created: %+v", p)
	}
	if a.provider("work").Host != "example.ghe.com" || a.clientForProvider("work") == nil {
		t.Fatal("adding Microsoft changed the GitHub provider")
	}
	if !tester.HasText("Sign in with Microsoft") || tester.HasText("Sign in with GitHub") {
		t.Fatal("Microsoft provider displayed GitHub sign-in")
	}
	if !tester.HasText("Use Copilot CLI sign-in") {
		t.Fatal("Microsoft provider did not offer the existing Copilot CLI sign-in")
	}
	if runtime.GOOS == "windows" {
		if !tester.HasText("Windows WAM sign-in does not require Copilot CLI") {
			t.Fatal("Windows Microsoft sign-in incorrectly requires Copilot CLI")
		}
	} else if !tester.HasText("On this platform, use a Copilot CLI work account") {
		t.Fatal("Microsoft sign-in did not explain this platform's CLI requirement")
	}
	if _, ok := tester.Find("Microsoft 365 service domain"); ok {
		t.Fatal("Microsoft provider must not display a service domain input")
	}
	if tester.HasText("Microsoft 365 service") {
		t.Fatal("Microsoft provider must not display a service configuration label")
	}
	saveProviderScreenshot(t, tester, "provider-microsoft.png")
	settingsClick(t, tester, "Provider name input")
	tester.Command("selectAll")
	tester.Type("My work account")
	if p.Name != "My work account" {
		t.Fatal("Microsoft provider name did not auto-save")
	}
	if p.Host != store.M365ProviderHost {
		t.Fatal("Microsoft provider name change modified the service domain")
	}
	loaded := settingsSavedState(t, a)
	stored := loaded.Settings.Providers[len(loaded.Settings.Providers)-1]
	if stored.Name != "My work account" || stored.Kind != store.ProviderKindM365 || stored.Host != store.M365ProviderHost {
		t.Fatalf("Microsoft provider was not persisted: %+v", stored)
	}
}

func saveProviderScreenshot(t *testing.T, tester *ui.Tester, name string) {
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
	if err := png.Encode(f, tester.Image()); err != nil {
		t.Fatal(err)
	}
}

func TestMicrosoftInteractiveSignInAndProviderIcons(t *testing.T) {
	a := providerTestApp(t)
	a.state.Settings.Providers = append(a.state.Settings.Providers, store.Provider{ID: "m365", Kind: store.ProviderKindM365, Name: "Microsoft 365 Copilot", Host: store.M365ProviderHost})
	a.providerSettingsID = "m365"
	r := a.providerState("m365")
	r.authBusy = true
	r.status = "Choose your work account in the Windows sign-in window…"
	tester := ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.providerSettings(c) }) }, 600, 1000)
	for _, label := range []string{"Cancel", "Microsoft 365 Copilot icon"} {
		if _, ok := tester.Find(label); !ok {
			t.Fatalf("Microsoft sign-in missing %q", label)
		}
	}
	if !tester.HasText(r.status) || tester.HasText("Open GitHub") || tester.HasText("Copy code") {
		t.Fatal("Microsoft sign-in must wait for interactive authorization without a device code")
	}
	settingsClick(t, tester, "Cancel")
	if r.authBusy || !tester.HasText("Sign in with Microsoft") {
		t.Fatal("Microsoft sign-in cannot be cancelled")
	}
	r.authBusy, r.status = true, "Connecting the selected Copilot CLI work account…"
	r.authCode = github.DeviceCode{}
	tester = ui.NewTester(func(c *ui.Context) { ui.Column(c).Fill().Children(func() { a.providerSettings(c) }) }, 600, 1000)
	if !tester.HasText(r.status) || tester.HasText("Requesting a sign-in code…") {
		t.Fatal("CLI connection incorrectly asks the user to wait for a device code")
	}

	icons := ui.NewTester(func(c *ui.Context) {
		ui.Row(c).Padding(8).Gap(8).Background(ui.Hex("#ffffff")).Children(func() {
			providerIcon(c, store.ProviderKindGitHub, 24).TextColor(ui.Hex("#333333"))
			providerIcon(c, store.ProviderKindM365, 24)
		})
	}, 80, 40)
	bounds, ok := icons.Find("Microsoft 365 Copilot icon")
	if !ok {
		t.Fatal("Microsoft provider icon missing")
	}
	colorPixels := 0
	for y := int(bounds.Y); y < int(bounds.Y+bounds.H); y++ {
		for x := int(bounds.X); x < int(bounds.X+bounds.W); x++ {
			pixel := icons.Image().RGBAAt(x, y)
			if pixel.A > 200 && max(pixel.R, pixel.G, pixel.B)-min(pixel.R, pixel.G, pixel.B) > 60 {
				colorPixels++
			}
		}
	}
	if colorPixels < 50 {
		t.Fatalf("Microsoft provider symbol lost its colors (%d colored pixels)", colorPixels)
	}
}

func TestMicrosoftProviderMinimumWindowLayout(t *testing.T) {
	a := providerTestApp(t)
	a.settingsPage, a.providerSettingsID = "Providers", "m365"
	a.state.Settings.Providers = append(a.state.Settings.Providers, store.Provider{ID: "m365", Kind: store.ProviderKindM365, Name: "Microsoft 365 Copilot", Host: store.M365ProviderHost})
	tester := ui.NewTester(a.settingsView, 700, 480)
	for _, label := range []string{"Enable provider", "Provider name input", "Sign in with Microsoft", "Use Copilot CLI sign-in"} {
		bounds, ok := tester.Find(label)
		if !ok {
			t.Fatalf("missing Microsoft provider control %q", label)
		}
		if bounds.X < 360 || bounds.Y < 48 || bounds.W <= 0 || bounds.H <= 0 || bounds.X+bounds.W > 700.5 || bounds.Y+bounds.H > 480.5 {
			t.Errorf("Microsoft provider control %q overlaps navigation or leaves the minimum window: %+v", label, bounds)
		}
	}
	saveProviderScreenshot(t, tester, "provider-microsoft-minimum.png")
}
