package desktop

import (
	"slices"

	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

// Small stroke icons keep a consistent size with the original model picker.
var (
	// Tabler's pinned outline matches the app's other Tabler controls (MIT).
	modelPinIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M9 4v6l-2 4v2h10v-2l-2 -4v-6M12 16v5M8 4h8"/></svg>`))
	modelEyeIcon   = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7"><path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12Z"/><circle cx="12" cy="12" r="3"/></svg>`))
	modelBrainIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><path d="M12 18V5a3 3 0 0 0-5.8-1A4 4 0 0 0 3 10a4 4 0 0 0 1 7.5A4 4 0 0 0 12 18Zm0 0V5a3 3 0 0 1 5.8-1A4 4 0 0 1 21 10a4 4 0 0 1-1 7.5A4 4 0 0 1 12 18ZM6 9l3 1M5 15l3-1m10-5-3 1m4 5-3-1"/></svg>`))
	modelCheckIcon = ui.MustParseSVG([]byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="m5 12 4 4L19 6"/></svg>`))
)

type modelPickerState struct {
	Open bool
}

func modelDisplayName(model provider.Model) string {
	if model.Name != "" {
		return model.Name
	}
	return model.ID
}

func modelCapabilityIcons(c *ui.Context, model provider.Model) {
	if model.Vision {
		ui.Icon(c, modelEyeIcon).Size(15, 15).Tooltip("Image input").TextColor(c.Theme().TextMuted)
	}
	if model.Reasoning {
		ui.Icon(c, modelBrainIcon).Size(15, 15).Tooltip("Reasoning").TextColor(c.Theme().TextMuted)
	}
	if model.Tools {
		ui.Icon(c, settingsIcons["MCP"]).Size(15, 15).Tooltip("Tool use").TextColor(c.Theme().TextMuted)
	}
	if model.PDF {
		ui.Text(c, "PDF").FontSize(10).TextColor(c.Theme().TextMuted).Tooltip("PDF input")
	}
}

func (a *app) modelPicker(c *ui.Context) {
	t := c.Theme()
	grouped := a.enabledProviderCount() > 1
	state := ui.Local(c.Root(), "top-model-picker", func() modelPickerState { return modelPickerState{} })
	if a.searchOpen {
		state.Open = false
	}
	label := a.selectedModel()
	if a.isWebChat(a.current()) {
		label = providerKindName(a.webChatKind(a.current()))
	} else if model, ok := a.modelForProvider(a.selectedProviderID(), label); ok {
		label = modelDisplayName(model)
	}
	if label == "" {
		label = "Choose a model"
	}
	providerLabel := "GitHub Copilot"
	if configured := a.provider(a.selectedProviderID()); configured != nil {
		providerLabel = configured.Name
	}
	trigger := ui.ButtonBase(c.Key("top-model-picker-trigger")).Label("Choose model").Height(42).Padding(4, 8).Gap(7).Radius(6).Background(ui.Transparent).Disabled(a.streaming)
	if trigger.Hovered() {
		trigger.Background(t.SurfaceHover)
	}
	trigger.Children(func() {
		ui.Column(c).MinWidth(0).Children(func() {
			ui.Text(c, label).FontSize(14).FontWeight(500).SingleLine()
			if grouped {
				ui.Text(c, providerLabel).FontSize(11).TextColor(t.TextMuted).SingleLine()
			}
		})
		ui.Icon(c, settingsChevron).Size(14, 14).TextColor(t.TextMuted)
	})
	trigger.OnClick(func() {
		state.Open = !state.Open
	})
	ui.PopoverBase(c.Key("top-model-picker-popup"), trigger, &state.Open, func(panel ui.Element) {
		width, height := c.Size()
		fill := ui.Hex("#eeeeef")
		if t.Dark {
			fill = ui.Hex("#272729")
		}
		panel.Label("Model options").Width(min(float32(360), width-24)).MaxHeight(min(float32(540), height-70)).Margin(6, 0, 0, 0).Padding(8).Gap(8).Radius(10).Background(fill).Border(1, t.Border).Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, .2))
		// MyGo overlays its scroll thumb and wider pointer track on the right
		// edge. Keep both clear of the pin button, including while dragging.
		scrollbarGutter := max(t.ScrollbarWidth, float32(6)) + 10
		ui.Scroll(c.Key("model-picker-list")).Label("Model list").MaxHeight(min(float32(510), height-100)).Padding(0, scrollbarGutter, 0, 0).Gap(6).Children(func() {
			count := 0
			for _, configured := range a.state.Settings.Providers {
				if store.IsWebProvider(configured.Kind) {
					continue
				}
				if a.clientForProvider(configured.ID) == nil {
					continue
				}
				models := a.providerModels(configured.ID)
				if len(models) == 0 {
					continue
				}
				slices.SortStableFunc(models, func(left, right provider.Model) int {
					lp, rp := slices.Contains(configured.PinnedModels, left.ID), slices.Contains(configured.PinnedModels, right.ID)
					if lp && !rp {
						return -1
					}
					if rp && !lp {
						return 1
					}
					return 0
				})
				if grouped {
					ui.Row(c.Key("group-"+configured.ID)).Padding(4, 8).Gap(7).Children(func() {
						providerIcon(c, configured.Kind, 16).TextColor(t.TextMuted)
						ui.Text(c, configured.Name).FontSize(12).FontWeight(500).TextColor(t.TextMuted).Grow(1).MinWidth(0).SingleLine().Tooltip(configured.Name)
						identity := configured.Host
						if configured.Login != "" {
							identity += " · " + configured.Login
						}
						ui.Text(c, identity).FontSize(10).TextColor(t.TextMuted).MaxWidth(140).SingleLine().Tooltip(identity)
					})
				}
				for _, model := range models {
					count++
					ui.Row(c.Key(configured.ID + "/" + model.ID)).FillWidth().Gap(2).Radius(6).Children(func() {
						selectButton := ui.ButtonBase(c).Grow(1).MinWidth(0).Height(32).Padding(5, 8).Gap(8).Radius(6).Justify(ui.Start).Label("Choose " + modelDisplayName(model) + " from " + configured.Name)
						if selectButton.Hovered() {
							selectButton.Background(t.SurfaceHover)
						}
						selectButton.Children(func() {
							if a.selectedProviderID() == configured.ID && a.selectedModel() == model.ID {
								ui.Icon(c, modelCheckIcon).Size(16, 16).TextColor(t.Success)
							} else {
								ui.Box(c).Size(16, 16)
							}
							ui.Text(c, modelDisplayName(model)).FontSize(13).Grow(1).MinWidth(0).SingleLine()
							modelCapabilityIcons(c, model)
						}).OnClick(func() { a.chooseModel(configured.ID, model.ID); state.Open = false })
						pinned := slices.Contains(configured.PinnedModels, model.ID)
						pinLabel := "Pin " + modelDisplayName(model) + " from " + configured.Name
						if pinned {
							pinLabel = "Unpin " + modelDisplayName(model) + " from " + configured.Name
						}
						pin := iconButton(c, modelPinIcon, pinLabel).Shrink(0)
						if pinned {
							pin.TextColor(t.Accent)
						} else {
							pin.TextColor(t.TextMuted)
						}
						pin.OnClick(func() { a.toggleModelPin(configured.ID, model.ID) })
					})
				}
			}
			webGroupShown := false
			for _, configured := range a.state.Settings.Providers {
				if !store.IsWebProvider(configured.Kind) || configured.Disabled {
					continue
				}
				if !webGroupShown {
					ui.Row(c.Key("group-web-chat")).Padding(4, 8).Gap(7).Children(func() {
						ui.Icon(c, iEmptyChat).Size(16, 16).TextColor(t.TextMuted)
						ui.Text(c, "Web Chat").FontSize(12).FontWeight(500).TextColor(t.TextMuted)
					})
					webGroupShown = true
				}
				count++
				selectButton := ui.ButtonBase(c.Key("web-provider-"+configured.ID)).FillWidth().MinWidth(0).Height(32).Padding(5, 8).Gap(8).Radius(6).Justify(ui.Start).Label("Choose " + configured.Name)
				if selectButton.Hovered() {
					selectButton.Background(t.SurfaceHover)
				}
				selectButton.Children(func() {
					if a.isWebChat(a.current()) && a.selectedProviderID() == configured.ID {
						ui.Icon(c, modelCheckIcon).Size(16, 16).TextColor(t.Success)
					} else {
						ui.Box(c).Size(16, 16)
					}
					providerIcon(c, configured.Kind, 16)
					ui.Text(c, configured.Name).FontSize(13).Grow(1).MinWidth(0).SingleLine()
				}).OnClick(func() {
					a.chooseWebProvider(configured.ID)
					state.Open = false
				})
			}
			if count == 0 {
				ui.Text(c, "No available models").Padding(12).TextColor(t.TextMuted)
				ui.Button(c, "Open Providers").OnClick(func() { state.Open = false; a.openSettings("Providers") })
			}
		})
	})
}
