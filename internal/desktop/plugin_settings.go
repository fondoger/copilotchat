package desktop

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/fondoger/copilotchat/internal/plugin"
	"github.com/fondoger/copilotchat/internal/plugininstall"
	"github.com/fondoger/copilotchat/internal/store"
)

type pluginOperations struct {
	cancel              context.CancelFunc
	source, ref, status string
	auto                bool
}

func (a *app) initPlugins() {
	a.pluginManager = plugininstall.New(filepath.Join(a.dataDir, "plugins"))
	entries, err := a.pluginManager.List()
	if err != nil {
		a.pluginOps.status = err.Error()
		return
	}
	a.installedPlugins = entries
}

// Serialize package changes and only replace clients whose installed code
// changed. A check that finds the same revision must not interrupt a stream.
func (a *app) runPluginOp(label string, work func(context.Context) error) {
	if a.pluginManager == nil || a.quitting || a.pluginOps.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.pluginOps.cancel = cancel
	a.pluginOps.status = label + "…"
	go func() {
		defer cancel()
		err := work(ctx)
		entries, listErr := a.pluginManager.List()
		a.update(func() {
			if a.quitting {
				return
			}
			a.pluginOps.cancel = nil
			if listErr == nil {
				old := a.installedPlugins
				a.installedPlugins = entries
				revisions := make(map[string]plugininstall.Entry, len(old))
				for _, e := range old {
					revisions[e.ID] = e
				}
				for _, e := range entries {
					if r, ok := revisions[e.ID]; !ok || r.Revision != e.Revision || r.Source != e.Source || r.RepoID != e.RepoID {
						a.reloadPluginProviders(e.ID)
					}
					delete(revisions, e.ID)
				}
				for id := range revisions {
					a.reloadPluginProviders(id)
				}
			}
			if e := errors.Join(err, listErr); e != nil {
				a.pluginOps.status = e.Error()
			} else {
				a.pluginOps.status = ""
			}
		})
	}()
}

func (a *app) checkDuePluginUpdates(now time.Time) {
	if a.quitting || a.pluginOps.cancel != nil || a.pluginManager == nil {
		return
	}
	s := &a.state.Settings
	hours := s.UpdateIntervalHours
	if hours <= 0 {
		hours = store.DefaultUpdateIntervalHours
	}
	if !s.LastPluginCheck.IsZero() && !now.Before(s.LastPluginCheck) && now.Sub(s.LastPluginCheck) < time.Duration(hours)*time.Hour {
		return
	}
	var ids []string
	for _, e := range a.installedPlugins {
		if e.Auto {
			ids = append(ids, e.ID)
		}
	}
	if len(ids) == 0 {
		return
	}
	s.LastPluginCheck = now
	a.save()
	a.runPluginOp("Checking plugin updates", func(ctx context.Context) error {
		var errs []error
		for _, id := range ids {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if _, err := a.pluginManager.Update(ctx, id); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", id, err))
			}
		}
		return errors.Join(errs...)
	})
}

func (a *app) installPlugin() {
	source, ref, auto := strings.TrimSpace(a.pluginOps.source), strings.TrimSpace(a.pluginOps.ref), a.pluginOps.auto
	parent := a.settingsWin
	a.runPluginOp("Preparing plugin", func(ctx context.Context) error {
		_, err := a.pluginManager.InstallReviewed(ctx, source, ref, auto, func(ctx context.Context, m plugin.Manifest) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			detail := fmt.Sprintf("%s (%s), version %s\nSource: %s\n\nMay send chats to:\n%s", m.Name, m.ID, m.Version, source, strings.Join(m.Network, "\n"))
			if m.WAMResource != "" {
				detail += "\n\nWindows sign-in application: " + m.WAMClientID + "\nResource: " + m.WAMResource + "\nThe listed network origins may receive the sign-in token."
			}
			if auto {
				detail += "\n\nAutomatic updates enabled."
			}
			res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: parent, Title: "Install provider plugin?", Message: "Install provider plugin?", Detail: detail, Type: mygo.MessageWarning, Buttons: []string{"Cancel", "Install"}, DefaultButton: 0, CancelButton: 0})
			if err != nil {
				return err
			}
			if res.Button != 1 {
				return errors.New("Installation canceled.")
			}
			return ctx.Err()
		})
		return err
	})
}

func (a *app) choosePluginSource(directory bool) {
	opts := mygo.OpenDialogOptions{Parent: a.settingsWin, Directory: directory, Title: "Choose plugin folder"}
	if !directory {
		opts.Title = "Choose plugin archive"
		opts.Filters = []mygo.FileFilter{{Name: "ZIP archives", Extensions: []string{"zip"}}}
	}
	paths, err := mygo.Dialog.Open(opts)
	if err != nil {
		a.pluginOps.status = err.Error()
	} else if len(paths) > 0 {
		a.pluginOps.source, a.pluginOps.ref = paths[0], ""
	}
}
func (a *app) pluginSettings(c *ui.Context) {
	p := &a.pluginOps
	busy := p.cancel != nil
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, "Install a provider").FontWeight(600)
		ui.TextInput(c.Key("plugin-source"), &p.source).Label("Plugin source").Placeholder("owner/repo, GitHub URL or local path").FillWidth().Disabled(busy)
		ui.Row(c).Gap(6).Children(func() {
			ui.Button(c.Key("choose-plugin-folder"), "Choose folder…").Disabled(busy).OnClick(func() { a.choosePluginSource(true) })
			ui.Button(c.Key("choose-plugin-zip"), "Choose ZIP…").Disabled(busy).OnClick(func() { a.choosePluginSource(false) })
		})
		ui.TextInput(c.Key("plugin-ref"), &p.ref).Label("GitHub revision").Placeholder("Branch, tag or commit (optional)").FillWidth().Disabled(busy)
		ui.Checkbox(c.Key("install-auto"), &p.auto, "Automatically update this plugin").Disabled(busy)
		ui.Button(c.Key("install-plugin"), "Review and install…").Disabled(busy || strings.TrimSpace(p.source) == "").OnClick(a.installPlugin)
	})
	if p.status != "" {
		ui.Text(c, p.status).Selectable().TextColor(c.Theme().TextMuted)
	}
	for _, entry := range a.installedPlugins {
		ui.Column(c.Key("plugin-" + entry.ID)).Gap(6).Children(func() {
			ui.Text(c, entry.Name).FontWeight(600)
			source := entry.Source
			if entry.Ref != "" {
				source += " @ " + entry.Ref
			}
			ui.Text(c, source).FontSize(12).TextColor(c.Theme().TextMuted).Selectable()
			rev := entry.Revision
			if len(rev) > 12 {
				rev = rev[:12]
			}
			ui.Text(c, "Installed revision: "+rev).FontSize(12)
			auto := entry.Auto
			ui.Checkbox(c.Key("auto-"+entry.ID), &auto, "Automatic updates").Disabled(busy).OnChange(func() {
				a.runPluginOp("Saving update preference", func(context.Context) error { return a.pluginManager.SetAuto(entry.ID, auto) })
			})
			ui.Row(c).Gap(8).Children(func() {
				ui.Button(c, "Update now").Disabled(busy).OnClick(func() {
					a.runPluginOp("Updating "+entry.Name, func(ctx context.Context) error { _, err := a.pluginManager.Update(ctx, entry.ID); return err })
				})
				ui.Button(c, "Uninstall…").Disabled(busy).OnClick(func() {
					res, err := mygo.Dialog.Message(mygo.MessageOptions{Parent: a.settingsWin, Title: "Uninstall plugin?", Message: "Uninstall " + entry.Name + "?", Detail: "Its provider accounts will be unavailable. Your conversations will be kept.", Type: mygo.MessageWarning, Buttons: []string{"Cancel", "Uninstall"}, DefaultButton: 0, CancelButton: 0})
					if err == nil && res.Button == 1 {
						a.runPluginOp("Removing "+entry.Name, func(context.Context) error { return a.pluginManager.Remove(entry.ID) })
					}
				})
			})
		})
	}
	if slices.ContainsFunc(a.installedPlugins, func(e plugininstall.Entry) bool { return e.Auto }) {
		ui.Text(c, "Update frequency is set in General.").FontSize(12).TextColor(c.Theme().TextMuted)
	}
}
