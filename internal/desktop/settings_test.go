package desktop

import (
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/mcp"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/store"
)

func newSettingsTestApp(t *testing.T) *app {
	t.Helper()
	storage, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a := &app{
		storage: storage, state: store.DefaultState(), settingsPage: "General",
		providers: map[string]*providerRuntime{store.DefaultProviderID: {
			client: github.NewClient("headless-test-token"),
			models: []provider.Model{{ID: "gpt-4o", Name: "GPT-4o"}, {ID: "claude-sonnet-4", Name: "Claude Sonnet 4"}},
			status: "Connected · 2 models available",
		}},
	}
	a.state.Settings.Model = "gpt-4o"
	a.state.Settings.MCPServers = []mcp.Config{{ID: "example", Name: "Example server", Transport: "http", URL: "http://localhost:3000/mcp", Enabled: false}}
	a.refreshMCPJSON()
	a.save()
	return a
}

func settingsClick(t *testing.T, tester *ui.Tester, label string) {
	t.Helper()
	if err := tester.Click(label); err != nil {
		t.Fatalf("click %q: %v; visible text: %q", label, err, tester.Texts())
	}
}

func settingsSavedState(t *testing.T, a *app) store.State {
	t.Helper()
	state, err := a.storage.Load()
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func TestReferenceIconsRender(t *testing.T) {
	entries, err := iconAssets.ReadDir("assets/icons")
	if err != nil {
		t.Fatal(err)
	}
	var icons []*ui.SVG
	var names []string
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".svg") {
			continue
		}
		data, err := iconAssets.ReadFile("assets/icons/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		icon, err := ui.ParseSVG(data)
		if err != nil {
			t.Fatalf("parse %s: %v", entry.Name(), err)
		}
		icons = append(icons, icon)
		names = append(names, entry.Name())
	}
	tester := ui.NewTester(func(c *ui.Context) {
		ui.Column(c).Fill().Padding(8).Gap(8).Background(ui.Hex("#ffffff")).Children(func() {
			for row := 0; row < len(icons); row += 6 {
				ui.Row(c).Height(24).Gap(8).Children(func() {
					for i := row; i < min(row+6, len(icons)); i++ {
						ui.Icon(c, icons[i]).Size(24, 24).TextColor(ui.Hex("#000000")).Label(names[i])
					}
				})
			}
		})
	}, 208, 16+32*((len(icons)+5)/6))
	for _, name := range names {
		bounds, ok := tester.Find(name)
		if !ok {
			t.Fatalf("missing rendered icon %s", name)
		}
		pixels := 0
		for y := int(bounds.Y); y < int(bounds.Y+bounds.H); y++ {
			for x := int(bounds.X); x < int(bounds.X+bounds.W); x++ {
				pixel := tester.Image().RGBAAt(x, y)
				if pixel.A > 0 && pixel.R < 220 && pixel.G < 220 && pixel.B < 220 {
					pixels++
				}
			}
		}
		if pixels < 4 {
			t.Errorf("%s did not draw visible SVG geometry (%d pixels)", name, pixels)
		}
	}
}

func TestSettingsInvalidMCPDoesNotReplaceServers(t *testing.T) {
	a := newSettingsTestApp(t)
	a.settingsPage = "MCP"
	before := settingsSavedState(t, a).Settings.MCPServers
	tester := ui.NewTester(a.settingsView, 900, 620)
	settingsClick(t, tester, "MCP server configuration JSON")
	tester.Command("selectAll")
	tester.Type(`[{"id":"broken","transport":"stdio"}]`)
	settingsClick(t, tester, "Save configuration")
	if !strings.Contains(a.mcpStatus, "needs both id and name") {
		t.Fatalf("missing useful validation error: %q", a.mcpStatus)
	}
	if !reflect.DeepEqual(a.state.Settings.MCPServers, before) {
		t.Fatalf("invalid configuration changed current servers: %#v", a.state.Settings.MCPServers)
	}
	if saved := settingsSavedState(t, a).Settings.MCPServers; !reflect.DeepEqual(saved, before) {
		t.Fatalf("invalid configuration changed saved servers: %#v", saved)
	}
	// A toggle next to the editor must not throw away a draft that failed validation.
	draft := a.mcpJSON
	settingsClick(t, tester, "Enable Example server")
	if a.mcpJSON != draft || a.state.Settings.MCPServers[0].Enabled {
		t.Fatal("server toggle changed data while invalid configuration was being edited")
	}
	settingsClick(t, tester, "MCP server configuration JSON")
	tester.Command("selectAll")
	tester.Type(`[{"id":"replacement","name":"Replacement","transport":"http","url":"https://example.invalid/mcp","enabled":false}]`)
	settingsClick(t, tester, "Save configuration")
	if saved := settingsSavedState(t, a).Settings.MCPServers; len(saved) != 1 || saved[0].ID != "replacement" {
		t.Fatalf("corrected configuration was not saved: %#v (%s)", saved, a.mcpStatus)
	}
}

func TestSettingsMinimumWindowLayout(t *testing.T) {
	for _, page := range []struct{ name, control string }{
		{"General", "Enter"},
		{"Appearance", "Chat font size"},
		{"Providers", "Provider name input"},
		{"MCP", "MCP server configuration JSON"},
		{"Extensions", "Enable Chat Title"},
	} {
		t.Run(page.name, func(t *testing.T) {
			a := newSettingsTestApp(t)
			a.settingsPage = page.name
			tester := ui.NewTester(a.settingsView, 700, 480)
			for _, label := range []string{page.name, page.control} {
				bounds, ok := tester.Find(label)
				if !ok {
					t.Fatalf("missing %q", label)
				}
				if bounds.X < 0 || bounds.Y < 0 || bounds.W <= 0 || bounds.H <= 0 || bounds.X+bounds.W > 700.5 || bounds.Y+bounds.H > 480.5 {
					t.Errorf("%q outside minimum window: %+v", label, bounds)
				}
			}
			if bounds, ok := tester.Find(page.control); ok && bounds.X < 160 {
				t.Errorf("content overlaps settings navigation: %+v", bounds)
			}
		})
	}
}

func TestSettingsScreenshots(t *testing.T) {
	dir := os.Getenv("COPILOT_CHAT_SCREENSHOTS")
	if dir == "" {
		t.Skip("set COPILOT_CHAT_SCREENSHOTS to an output directory to render settings images")
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	for _, page := range []string{"General", "Appearance", "Providers", "LLM Gateway", "MCP", "Extensions"} {
		t.Run(page, func(t *testing.T) {
			a := newSettingsTestApp(t)
			a.settingsPage = page
			tester := ui.NewTester(a.settingsView, 900, 620)
			output := filepath.Join(dir, "settings-"+strings.ToLower(page)+".png")
			f, err := os.Create(output)
			if err != nil {
				t.Fatal(err)
			}
			if err := png.Encode(f, tester.Image()); err != nil {
				_ = f.Close()
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
