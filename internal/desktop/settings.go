package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/mcp"
)

var settingsIcons = map[string]*ui.SVG{
	"General":    iSettings,
	"Appearance": loadIcon("ion-color-palette-outline"),
	"Providers":  loadIcon("ion-hardware-chip-outline"),
	"MCP":        loadIcon("f7-hammer"),
	"Extensions": loadIcon("ion-extension-puzzle-outline"),
}

var settingsChevron = loadIcon("tabler-chevron-down")

func (a *app) settingsView(c *ui.Context) {
	t := a.theme(c)
	t.FontSize = 14
	if !t.Dark {
		t.Surface = ui.Hex("#ffffff")
	}
	c.SetTheme(t)
	nav, pane, selected := ui.Hex("#e2e1e2"), ui.Hex("#efeeef"), ui.Hex("#d4d4d8")
	navBorder, headerBorder := ui.Hex("#d4d4d8"), ui.Hex("#e4e4e7")
	if t.Dark {
		nav, pane, selected = ui.Hex("#262626"), ui.Hex("#1e1e1e"), ui.Hex("#52525b").Alpha(.7)
		navBorder, headerBorder = ui.Hex("#000000"), ui.Hex("#3f3f46")
	}
	c.OnShortcut(0, ui.KeyEscape, func() {
		if a.settingsWin != nil {
			a.settingsWin.Close()
		}
	})
	ui.Row(c).Fill().AlignItems(ui.Stretch).Background(pane).Children(func() {
		ui.Column(c).Width(160).Shrink(0).Background(nav).BorderWidth(0, 1, 0, 0).BorderColor(navBorder).Children(func() {
			ui.Row(c).Height(48).PaddingX(20).PaddingStart(max(float32(20), c.TitleBar().Left)).DragWindow().Children(func() {
				ui.Text(c, "Settings").FontSize(14).FontWeight(500).TextColor(t.Text.Alpha(.75))
			})
			ui.Column(c).Padding(12, 12, 20, 12).Gap(4).Children(func() {
				for _, page := range []string{"General", "Appearance", "Providers", "MCP", "Extensions"} {
					button := ui.ButtonBase(c.Key(page)).FillWidth().Height(28).Padding(4, 8).Gap(8).Radius(6).Justify(ui.Start).AlignItems(ui.Center).Label(page + " settings")
					if a.settingsPage == page {
						button.Background(selected).TextColor(t.Accent)
					} else {
						button.TextColor(t.Text).Opacity(.6)
						if button.Hovered() {
							button.Background(selected)
						}
					}
					button.Children(func() {
						ui.Icon(c, settingsIcons[page]).Size(20, 20).Shrink(0)
						ui.Text(c, page).FontSize(14).FontWeight(500).SingleLine()
					}).OnClick(func() { a.settingsPage = page })
				}
			})
		})
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Height(48).Shrink(0).PaddingX(20).PaddingEnd(max(float32(20), c.TitleBar().Right)).DragWindow().BorderWidth(0, 0, 1, 0).BorderColor(headerBorder).Children(func() {
				ui.Text(c, a.settingsPage).FontSize(14).FontWeight(600).TextColor(t.Text.Alpha(.75))
			})
			switch a.settingsPage {
			case "Providers":
				a.providersPage(c)
			case "MCP":
				a.mcpSettings(c)
			case "Extensions":
				a.settingsDetail(c, "Chat Title", a.extensionSettings)
			default:
				ui.Scroll(c.Key(a.settingsPage)).Grow(1).Padding(20).Gap(16).Children(func() {
					if a.settingsPage == "Appearance" {
						a.appearanceSettings(c)
					} else {
						a.generalSettings(c)
					}
				})
			}
		})
	})
}

func settingsCard(c *ui.Context, children func()) {
	t := c.Theme()
	fill := ui.Hex("#fafafa")
	border, divider := c.Theme().Border, c.Theme().Border
	if t.Dark {
		fill = ui.RGBA(50, 49, 49, .73)
		border, divider = ui.Hex("#52525b").Alpha(.7), ui.Hex("#3f3f46")
	}
	ui.Column(c).PaddingX(10).Radius(8).Background(fill).Border(1, border).Dividers(1, divider).Children(children)
}

func settingsRow(c *ui.Context, label, description string, control func()) {
	ui.Row(c).PaddingY(10).Gap(12).MinHeight(46).Children(func() {
		ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
			ink := ui.Hex("#27272a")
			if c.Theme().Dark {
				ink = ui.Hex("#e4e4e7")
			}
			ui.Text(c, label).FontSize(14).FontWeight(500).TextColor(ink)
			if description != "" {
				ui.Text(c, description).FontSize(12).TextColor(c.Theme().TextMuted)
			}
		})
		ui.Row(c).Width(210).Shrink(0).Justify(ui.End).Children(control)
	})
}

func (a *app) generalSettings(c *ui.Context) {
	settingsCard(c, func() {
		settingsRow(c, "Send message with", "Shift+Enter always inserts a new line.", func() {
			shortcut := "Ctrl+Enter"
			if a.state.Settings.SendWithEnter {
				shortcut = "Enter"
			}
			ui.Select(c.Key("send-key"), &shortcut, []string{"Enter", "Ctrl+Enter"}).Height(26).Label("Send message with").OnChange(func() {
				a.state.Settings.SendWithEnter = shortcut == "Enter"
				a.saveSettings()
			})
		})
		settingsRow(c, "Keep running in the tray", "Closing the chat window keeps the app available from the system tray.", func() {
			ui.Switch(c.Key("tray"), &a.state.Settings.StayInTray).Label("Keep running in the tray").OnChange(func() {
				a.syncTray()
				a.saveSettings()
			})
		})
	})
	a.updateSettings(c)
}

func (a *app) appearanceSettings(c *ui.Context) {
	settingsCard(c, func() {
		settingsRow(c, "Appearance", "", func() {
			appearance := map[string]string{"light": "Light", "dark": "Dark", "system": "System"}[a.state.Settings.Theme]
			if appearance == "" {
				appearance = "System"
			}
			ui.Select(c.Key("appearance"), &appearance, []string{"Light", "Dark", "System"}).Height(26).Label("Appearance").OnChange(func() {
				a.state.Settings.Theme = strings.ToLower(appearance)
				a.applyAppearance()
				a.saveSettings()
			})
		})
		settingsRow(c, "Chat font size", "Uses the system font for clear native text.", func() {
			size := strconv.Itoa(a.state.Settings.FontSize)
			ui.Select(c.Key("font-size"), &size, []string{"12", "13", "14", "15", "16", "18", "20", "24", "28", "32"}).Height(26).Label("Chat font size").OnChange(func() {
				if value, err := strconv.Atoi(size); err == nil {
					a.state.Settings.FontSize = value
					a.saveSettings()
				}
			})
		})
	})
	ui.Column(c).Padding(16).Gap(8).Radius(8).Border(1, c.Theme().Border).Background(c.Theme().Background).Children(func() {
		ui.Text(c, "Preview").FontSize(12).TextColor(c.Theme().TextMuted)
		ui.Text(c, "A little space to think, write, and explore.").FontSize(float32(a.state.Settings.FontSize))
	})
}

func (a *app) settingsDetail(c *ui.Context, name string, detail func(*ui.Context)) {
	t := c.Theme()
	fill, selected := ui.Hex("#ffffff"), ui.Hex("#e4e4e7")
	if t.Dark {
		fill, selected = ui.Hex("#1b1b1e"), ui.Hex("#44444b")
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Column(c).Width(200).Shrink(0).Padding(8).Background(fill).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			ui.Row(c).Height(28).PaddingX(8).Radius(6).Background(selected).TextColor(t.Accent).Children(func() {
				ui.Text(c, name).SingleLine()
			})
		})
		ui.Column(c).Grow(1).MinWidth(0).Children(func() {
			ui.Row(c).Height(36).PaddingX(16).Shrink(0).BorderWidth(0, 0, 1, 0).BorderColor(t.Border).Children(func() {
				ui.Text(c, name).FontWeight(500)
			})
			ui.Scroll(c.Key(name)).Grow(1).Padding(16).Gap(16).Children(func() { detail(c) })
		})
	})
}

func (a *app) extensionSettings(c *ui.Context) {
	ui.Text(c, "Automatically name new chats after the first response.").TextColor(c.Theme().TextMuted)
	ui.Row(c).Gap(12).Children(func() {
		ui.Text(c, "Auto update chat title").Grow(1).FontWeight(500)
		ui.Switch(c.Key("chat-title"), &a.state.Settings.ChatTitle).Label("Enable Chat Title").OnChange(a.saveSettings)
	})
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, "Model for chat title generation").FontWeight(500)
		a.settingsModelSelect(c, "title-model", &a.state.Settings.TitleModel, true).Disabled(!a.state.Settings.ChatTitle)
	})
}

func (a *app) mcpSettings(c *ui.Context) {
	t := c.Theme()
	saved, _ := json.Marshal(a.state.Settings.MCPServers)
	if string(saved) == "null" {
		saved = []byte("[]")
	}
	var draft any
	draftBytes := []byte(a.mcpJSON)
	if json.Unmarshal(draftBytes, &draft) == nil {
		draftBytes, _ = json.Marshal(draft)
	}
	// A server toggle must not discard text still being edited in the JSON pane.
	var savedValue any
	_ = json.Unmarshal(saved, &savedValue)
	savedBytes, _ := json.Marshal(savedValue)
	dirty := string(draftBytes) != string(savedBytes)
	fill := ui.Hex("#ffffff")
	if t.Dark {
		fill = ui.Hex("#1b1b1e")
	}
	ui.Row(c).Grow(1).MinHeight(0).AlignItems(ui.Stretch).Children(func() {
		ui.Scroll(c.Key("mcp-servers")).Width(200).Shrink(0).Padding(8).Gap(8).Background(fill).BorderWidth(0, 1, 0, 0).BorderColor(t.Border).Children(func() {
			if len(a.state.Settings.MCPServers) == 0 {
				ui.Text(c, "No MCP servers").Padding(8).TextColor(t.TextMuted)
			}
			for _, server := range a.state.Settings.MCPServers {
				ui.Column(c.Key(server.ID)).Padding(8).Gap(6).Radius(6).Background(t.Surface).Children(func() {
					ui.Text(c, server.Name).SingleLine().FontWeight(500)
					ui.Row(c).Gap(8).Children(func() {
						status := "Disabled"
						if server.Enabled {
							status = "Enabled"
						}
						ui.Text(c, status).FontSize(12).Grow(1).TextColor(t.TextMuted)
						enabled := server.Enabled
						ui.Switch(c, &enabled).Disabled(dirty).Tooltip("Save configuration before changing server state").Label("Enable " + server.Name).OnChange(func() {
							for i := range a.state.Settings.MCPServers {
								if a.state.Settings.MCPServers[i].ID == server.ID {
									a.state.Settings.MCPServers[i].Enabled = enabled
								}
							}
							a.refreshMCPJSON()
							a.saveSettings()
						})
					})
					ui.Button(c, "Test connection").FontSize(12).Height(26).Disabled(a.mcpBusy || dirty).OnClick(func() { a.testMCP([]mcp.Config{server}) })
				})
			}
		})
		ui.Scroll(c.Key("mcp-config")).Grow(1).MinWidth(0).Padding(16).Gap(12).Children(func() {
			ui.Text(c, "Server configuration").FontWeight(500)
			ui.Text(c, "Add servers as a JSON array. Use stdio for a local command or http for a remote MCP URL.").FontSize(12).TextColor(t.TextMuted)
			ui.TextArea(c.Key("mcp-json"), &a.mcpJSON).Height(260).Font("Consolas").FontSize(12).Label("MCP server configuration JSON").Placeholder("[]")
			ui.Row(c).Gap(8).Wrap().Children(func() {
				ui.PrimaryButton(c, "Save configuration").OnClick(a.saveMCP)
				label := "Test enabled servers"
				if a.mcpBusy {
					label = "Testing…"
				}
				ui.Button(c, label).Disabled(a.mcpBusy || dirty || len(a.enabledServers()) == 0).OnClick(func() { a.testMCP(a.enabledServers()) })
			})
			if a.mcpStatus != "" {
				ui.Text(c, a.mcpStatus).FontSize(12).TextColor(t.TextMuted).Selectable()
			}
			ui.Text(c, `Example: [{"id":"local","name":"My server","transport":"http","url":"http://localhost:3000/mcp","enabled":false}]`).Font("Consolas").FontSize(12).TextColor(t.TextMuted).Selectable()
		})
	})
}

func (a *app) refreshMCPJSON() {
	data, err := json.MarshalIndent(a.state.Settings.MCPServers, "", "  ")
	if err == nil {
		a.mcpJSON = string(data)
		if a.mcpJSON == "null" {
			a.mcpJSON = "[]"
		}
	}
}

func parseMCPSettings(input string) ([]mcp.Config, error) {
	var servers []mcp.Config
	decoder := json.NewDecoder(strings.NewReader(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&servers); err != nil {
		return nil, fmt.Errorf("Invalid configuration: %w", err)
	}
	if servers == nil {
		return nil, fmt.Errorf("Configuration must be a JSON array; use [] for no servers")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, fmt.Errorf("Configuration must contain one JSON array")
	}
	ids := map[string]bool{}
	for i := range servers {
		server := &servers[i]
		server.ID, server.Name = strings.TrimSpace(server.ID), strings.TrimSpace(server.Name)
		if server.ID == "" || server.Name == "" {
			return nil, fmt.Errorf("Server %d needs both id and name", i+1)
		}
		if ids[server.ID] {
			return nil, fmt.Errorf("Server id %q is used more than once", server.ID)
		}
		ids[server.ID] = true
		server.Transport = strings.ToLower(strings.TrimSpace(server.Transport))
		switch server.Transport {
		case "stdio":
			if strings.TrimSpace(server.Command) == "" {
				return nil, fmt.Errorf("%s: command is required for stdio", server.Name)
			}
		case "http", "streamable-http":
			parsed, err := url.Parse(server.URL)
			if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return nil, fmt.Errorf("%s: enter an http or https URL", server.Name)
			}
		default:
			return nil, fmt.Errorf("%s: transport must be stdio or http", server.Name)
		}
	}
	return servers, nil
}

func (a *app) saveMCP() {
	servers, err := parseMCPSettings(a.mcpJSON)
	if err != nil {
		a.mcpStatus = err.Error()
		return
	}
	a.state.Settings.MCPServers = servers
	a.refreshMCPJSON()
	a.saveSettings()
	a.mcpStatus = fmt.Sprintf("Saved %d MCP server(s).", len(servers))
}

func (a *app) testMCP(servers []mcp.Config) {
	if a.mcpBusy || len(servers) == 0 {
		return
	}
	a.mcpBusy, a.mcpStatus = true, "Connecting and discovering tools…"
	go func() {
		var results []string
		for _, server := range servers {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			tools, err := mcp.Discover(ctx, server)
			cancel()
			if err != nil {
				results = append(results, server.Name+": "+err.Error())
			} else {
				results = append(results, fmt.Sprintf("%s: connected · %d tools", server.Name, len(tools)))
			}
		}
		a.update(func() { a.mcpBusy = false; a.mcpStatus = strings.Join(results, "\n") })
	}()
}
