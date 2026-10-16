package desktop

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/markdown"
	"github.com/fondoger/copilotchat/internal/appupdate"
	"github.com/fondoger/copilotchat/internal/mcp"
	"github.com/fondoger/copilotchat/internal/store"
)

const version = "0.3.1"

type toolApproval struct {
	Server, Name, Arguments string
	Reply                   chan bool
}

type chatDraft struct {
	Text        string
	Attachments []store.Attachment
}

// All application state is owned by the UI thread. Workers publish through update.
type app struct {
	win, settingsWin               *mygo.Window
	tray                           *mygo.Tray
	state                          store.State
	storage                        *store.Store
	dataDir                        string
	quitting                       bool
	updater                        *appupdate.Manager
	updateCancel                   context.CancelFunc
	updateError                    string
	updateQuitWaiting              bool
	draft, query, status           string
	attachments                    []store.Attachment
	drafts                         map[string]chatDraft
	sidebarHidden, searchOpen      bool
	composer                       ui.Handle
	list                           ui.ListState
	md                             markdown.Options
	streaming                      bool
	streamChat, streamMessage      string
	cancel                         context.CancelFunc
	providers                      map[string]*providerRuntime
	providerSettingsID             string
	providerSecrets                credentialStore
	providerRefreshCancel          context.CancelFunc
	titleJobs                      map[string]*titleJob
	preview                        *artifactPreview
	settingsPage                   string
	mcpJSON, mcpStatus             string
	mcpBusy                        bool
	approval                       *toolApproval
	approvalOpen                   bool
	renameID, renameText, deleteID string
	renameOpen, deleteOpen         bool
	editMessageID, editText        string
}

func Run(dataDir string) error {
	mygo.App.SetName("Copilot Chat")
	mygo.App.SetVersion(version)
	// Plain Go builds and packaged builds must use the same own-app account.
	mygo.Secrets = mygo.Secrets.Service("Copilot Chat")
	if dataDir == "" {
		root, err := os.UserConfigDir()
		if err != nil {
			return err
		}
		dataDir = filepath.Join(root, "Copilot Chat")
	}
	s, err := store.New(dataDir)
	if err != nil {
		return err
	}
	state, loadErr := s.Load()
	if loadErr != nil {
		return fmt.Errorf("load conversations (original file preserved): %w", loadErr)
	}
	a := &app{storage: s, state: state, dataDir: dataDir, settingsPage: "General"}
	if !mygo.App.RequestSingleInstanceLock() {
		return nil
	}
	a.loadProviders()
	mygo.App.OnSecondInstance(func([]string, string) { a.show() })
	mygo.App.OnActivate(func(bool) { a.show() })
	mygo.App.OnBeforeQuit(func(event *mygo.QuitEvent) {
		if a.deferQuitForUpdate() {
			event.PreventDefault()
			return
		}
		a.quitting = true
		a.stopUpdates()
		a.closePreview()
		a.stop()
		a.cancelLogin()
		a.cancelModelRefresh()
		if a.providerRefreshCancel != nil {
			a.providerRefreshCancel()
		}
		a.cancelTitles()
		a.save()
	})
	mygo.App.WhenReady(func() {
		a.applyAppearance()
		a.win = mygo.NewWindow(mygo.WindowOptions{
			Title: "Copilot Chat", Width: 1060, Height: 720, MinWidth: 720, MinHeight: 480,
			TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 50, StateKey: "main", Content: ui.View(a.view),
		})
		a.win.OnClose(func(e *mygo.CloseEvent) {
			if !a.quitting && a.state.Settings.StayInTray && a.tray != nil {
				e.PreventDefault()
				if a.settingsWin != nil {
					a.settingsWin.Hide()
				}
				a.win.Hide()
				a.save()
			} else if !a.quitting {
				e.PreventDefault()
				mygo.App.Quit()
			}
		})
		a.syncTray()
		a.startProviderRefreshLoop()
		a.startUpdates()
	})
	mygo.App.OnQuit(func() {
		if a.tray != nil {
			a.tray.Destroy()
		}
	})
	return mygo.App.Run()
}

func newID() string { var b [12]byte; _, _ = rand.Read(b[:]); return hex.EncodeToString(b[:]) }

func (a *app) startProviderRefreshLoop() {
	a.refreshDueProviderModels(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	a.providerRefreshCancel = cancel
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				a.update(func() {
					if !a.quitting {
						a.refreshDueProviderModels(now)
					}
				})
			}
		}
	}()
}

func (a *app) update(fn func()) {
	if a.win == nil {
		return
	}
	a.win.Update(func() {
		fn()
		if a.settingsWin != nil {
			a.settingsWin.Invalidate()
		}
	})
}

func (a *app) save() {
	if a.storage == nil {
		return
	}
	if err := a.storage.Save(a.state); err != nil {
		a.status = "Could not save: " + err.Error()
	}
}

func (a *app) show() {
	if a.win != nil {
		a.win.Show()
		a.win.Restore()
		a.win.Focus()
	}
}

func (a *app) current() *store.Conversation {
	for i := range a.state.Conversations {
		if a.state.Conversations[i].ID == a.state.ActiveID {
			return &a.state.Conversations[i]
		}
	}
	return nil
}

func (a *app) conversation(id string) *store.Conversation {
	for i := range a.state.Conversations {
		if a.state.Conversations[i].ID == id {
			return &a.state.Conversations[i]
		}
	}
	return nil
}

func (a *app) newChat() {
	if a.current() != nil && len(a.current().Messages) == 0 && a.draft == "" && len(a.attachments) == 0 {
		a.composer.Focus()
		return
	}
	a.rememberDraft()
	a.closePreview()
	a.editMessageID, a.editText = "", ""
	now := time.Now()
	chat := store.Conversation{ID: newID(), Title: "New Chat", Model: a.state.Settings.Model, ProviderID: a.state.Settings.ActiveProviderID, ReasoningEffort: a.state.Settings.ReasoningEffort, ThinkingBudgetTokens: a.state.Settings.ThinkingBudgetTokens, CreatedAt: now, UpdatedAt: now}
	a.state.Conversations = append([]store.Conversation{chat}, a.state.Conversations...)
	a.state.ActiveID = chat.ID
	a.draft = ""
	a.attachments = nil
	a.list = ui.ListState{}
	a.save()
	a.composer.Focus()
}

func (a *app) rememberDraft() {
	if a.drafts == nil {
		a.drafts = make(map[string]chatDraft)
	}
	if a.draft == "" && len(a.attachments) == 0 {
		delete(a.drafts, a.state.ActiveID)
		return
	}
	a.drafts[a.state.ActiveID] = chatDraft{Text: a.draft, Attachments: append([]store.Attachment(nil), a.attachments...)}
}

func (a *app) selectChat(id string) {
	if a.state.ActiveID != id {
		a.closePreview()
		a.editMessageID, a.editText = "", ""
		a.rememberDraft()
		a.state.ActiveID = id
		draft := a.drafts[id]
		a.draft, a.attachments = draft.Text, append([]store.Attachment(nil), draft.Attachments...)
		a.list = ui.ListState{}
		a.save()
	}
	a.composer.Focus()
}

func (a *app) openSettings(page string) {
	if page != "" {
		a.settingsPage = page
	}
	b, _ := json.MarshalIndent(a.state.Settings.MCPServers, "", "  ")
	a.mcpJSON = string(b)
	if a.mcpJSON == "null" {
		a.mcpJSON = "[]"
	}
	if a.settingsWin != nil {
		a.settingsWin.Show()
		a.settingsWin.Focus()
		a.settingsWin.Invalidate()
		return
	}
	a.settingsWin = mygo.NewWindow(mygo.WindowOptions{Title: "Settings — Copilot Chat", Width: 900, Height: 620, MinWidth: 700, MinHeight: 480, TitleBarStyle: mygo.TitleBarHidden, TitleBarHeight: 48, Content: ui.View(a.settingsView)})
	a.settingsWin.OnClosed(func() { a.settingsWin = nil })
}

func (a *app) applyAppearance() { mygo.Theme.SetSource(mygo.ThemeSource(a.state.Settings.Theme)) }

func (a *app) theme(c *ui.Context) *ui.Theme {
	dark := c.Theme().Dark
	if a.state.Settings.Theme == "dark" {
		dark = true
	} else if a.state.Settings.Theme == "light" {
		dark = false
	}
	t := ui.LightTheme()
	if dark {
		t = ui.DarkTheme()
		t.Background = ui.Hex("#181818")
		t.Text = ui.Hex("#fafafa")
	} else {
		t.Text = ui.Hex("#09090b")
	}
	t.FontSize = 14
	t.Font = appFontFamily()
	t.Radius = 6
	t.Spacing = 4
	c.SetTheme(t)
	return t
}

func (a *app) saveSettings() {
	if !a.state.Settings.ChatTitle {
		a.cancelTitles()
	}
	a.save()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// enabledServers returns a value snapshot for a worker; views keep owning state.
func (a *app) enabledServers() []mcp.Config {
	var out []mcp.Config
	for _, s := range a.state.Settings.MCPServers {
		if s.Enabled {
			s.Args = append([]string(nil), s.Args...)
			if s.Env != nil {
				env := make(map[string]string, len(s.Env))
				for key, value := range s.Env {
					env[key] = value
				}
				s.Env = env
			}
			out = append(out, s)
		}
	}
	return out
}
