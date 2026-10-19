package desktop

import (
	"encoding/base64"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/store"
)

func attachmentCapabilityError(model provider.Model, known bool, mime string) error {
	name := model.Name
	if name == "" {
		name = "The current model"
	}
	if strings.HasPrefix(mime, "image/") && (!known || !model.Vision) {
		return fmt.Errorf("%s does not support image input. Choose a model with image support before adding an image.", name)
	}
	if mime == "application/pdf" && (!known || !model.PDF) {
		return fmt.Errorf("%s does not support PDF input. Choose a model with PDF support before adding a PDF.", name)
	}
	return nil
}

func (a *app) validateAttachments(providerID, modelID string, files []store.Attachment) error {
	model, known := a.modelForProvider(providerID, modelID)
	for _, f := range files {
		if err := attachmentCapabilityError(model, known, f.MIME); err != nil {
			return err
		}
	}
	return nil
}

func appendAttachment(files []store.Attachment, file store.Attachment) ([]store.Attachment, error) {
	if file.Size > maxAttachmentBytes {
		return files, fmt.Errorf("files must be 5 MB or smaller")
	}
	var total int64
	for _, f := range files {
		total += f.Size
	}
	if len(files) >= 10 || total+file.Size > 20*1024*1024 {
		return files, fmt.Errorf("maximum 10 attachments / 20 MB per message")
	}
	return append(files, file), nil
}

func (a *app) pasteAttachment(_ string, services ui.Services) bool {
	if !slices.Contains(services.ClipboardFormats(), transfer.PNG) {
		return false
	}
	model, known := a.modelForProvider(a.selectedProviderID(), a.selectedModel())
	if err := attachmentCapabilityError(model, known, "image/png"); err != nil {
		a.status = err.Error()
		return true
	}
	data, err := services.ReadClipboardData(transfer.PNG).Read(transfer.PNG)
	if err != nil || len(data) == 0 {
		a.status = "Could not read the image from the clipboard."
		return true
	}
	f := store.Attachment{Name: "Pasted image " + time.Now().Format("15-04-05") + ".png", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(data), Size: int64(len(data))}
	a.attachments, err = appendAttachment(a.attachments, f)
	if err != nil {
		a.status = err.Error()
	} else {
		a.status = ""
	}
	return true
}

func (a *app) reasoningSettings() (string, int) {
	if chat := a.current(); chat != nil {
		return chat.ReasoningEffort, chat.ThinkingBudgetTokens
	}
	return a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens
}

func thinkingBudgetRange(model provider.Model) (int, int, bool) {
	minimum, maximum := max(1, model.MinThinkingBudget), model.MaxThinkingBudget
	switch model.Endpoint {
	case "/v1/messages":
		if model.AdaptiveThinking {
			return 0, 0, false
		}
		minimum = max(1024, minimum)
		if model.MaxOutputTokens > 0 {
			maximum = min(maximum, model.MaxOutputTokens-1)
		}
	case "/chat/completions":
	default:
		return 0, 0, false
	}
	return minimum, maximum, maximum >= minimum
}

type thinkingOption struct{ Value, Label string }

func thinkingOptions(model provider.Model) []thinkingOption {
	options := []thinkingOption{{Value: "default", Label: "Default"}}
	if model.Endpoint != "/v1/messages" || model.AdaptiveThinking {
		for _, value := range model.ThinkingEfforts {
			options = append(options, thinkingOption{Value: value, Label: value})
		}
	}
	if minimum, maximum, valid := thinkingBudgetRange(model); valid && len(options) == 1 {
		seen := map[int]bool{}
		for _, n := range []int{minimum, 2048, 4096, 8192, 16384, maximum} {
			if n < minimum || n > maximum || seen[n] {
				continue
			}
			seen[n] = true
			options = append(options, thinkingOption{Value: "budget:" + strconv.Itoa(n), Label: fmt.Sprintf("%d tokens", n)})
		}
	}
	return options
}

func thinkingSettingsError(model provider.Model, effort string, budget int) error {
	if effort != "" && budget != 0 {
		return errors.New("Choose one thinking effort or budget before sending.")
	}
	if effort != "" && (!slices.Contains(model.ThinkingEfforts, effort) || (model.Endpoint == "/v1/messages" && !model.AdaptiveThinking)) {
		return errors.New("The saved thinking effort is not supported by this model. Choose Default or an available thinking effort.")
	}
	if budget != 0 {
		minimum, maximum, valid := thinkingBudgetRange(model)
		if !valid || budget < minimum || budget > maximum {
			return errors.New("The saved thinking budget is not supported by this model. Choose Default or an available thinking budget.")
		}
	}
	return nil
}

func (a *app) setReasoningSettings(effort string, budget int) {
	// Opening an older conversation does not change the default model for new
	// chats, so its thinking choice must not overwrite that model's defaults.
	if a.selectedProviderID() == a.state.Settings.ActiveProviderID && a.selectedModel() == a.state.Settings.Model {
		a.state.Settings.ReasoningEffort, a.state.Settings.ThinkingBudgetTokens = effort, budget
	}
	if chat := a.current(); chat != nil {
		chat.ReasoningEffort, chat.ThinkingBudgetTokens = effort, budget
	}
	a.save()
}

func (a *app) reasoningControl(c *ui.Context) {
	model, ok := a.modelForProvider(a.selectedProviderID(), a.selectedModel())
	if !ok {
		return
	}
	effort, budget := a.reasoningSettings()
	if !model.Reasoning && effort == "" && budget == 0 {
		return
	}
	selected := effort
	if selected == "" {
		selected = "default"
	}
	if budget > 0 {
		selected = "budget:" + strconv.Itoa(budget)
	}
	previous := selected
	options := thinkingOptions(model)
	if len(options) == 1 && selected == "default" {
		ui.Text(c, "Thinking").FontSize(11).TextColor(c.Theme().TextMuted).Tooltip("This model supports reasoning; adjustable effort was not advertised.")
		return
	}
	label := "Default"
	if effort != "" {
		label = effort
	}
	if budget != 0 {
		label = fmt.Sprintf("%d tokens", budget)
	}
	if thinkingSettingsError(model, effort, budget) != nil {
		label = "Unavailable"
	}
	selectBox := ui.SelectBase(c, &selected)
	selectBox.Trigger.Label("Thinking effort").Tooltip("Thinking effort").Height(26).Padding(3, 8).Gap(6).Radius(5).Disabled(a.streaming).Children(func() {
		ui.Text(c, "Thinking · "+label).FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Icon(c, iChevron).Size(10, 10).Rotate(90)
	})
	selectBox.Popup(func(panel ui.Element) {
		panel.Width(170).Padding(5).Radius(8).Background(c.Theme().Background).Border(1, c.Theme().Border).Shadow(0, 6, 20, 0, ui.RGBA(0, 0, 0, .18))
		for _, option := range options {
			item := selectBox.Item(option.Value).Padding(6, 9).Radius(4)
			if item.Highlighted() {
				item.Background(c.Theme().SurfaceHover)
			}
			item.Children(func() { ui.Text(c, option.Label).FontSize(12) })
		}
	})
	if selected != previous {
		effort, budget = "", 0
		if strings.HasPrefix(selected, "budget:") {
			budget, _ = strconv.Atoi(strings.TrimPrefix(selected, "budget:"))
		} else if selected != "default" {
			effort = selected
		}
		a.setReasoningSettings(effort, budget)
	}
}
