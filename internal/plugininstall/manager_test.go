package plugininstall

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/fondoger/copilotchat/internal/plugin"
)

// A top-level throw makes a runtime evaluation fail: installation only compiles.
const module = `throw new Error("must not run at install time"); export function catalog(){} export function models(){} export function inference(){} export function signIn(){ return host.wam.signIn() }`

func manifest(id, version, origin, wam string) string {
	m := plugin.Manifest{ID: id, Name: "Example", Version: version, Main: "index.js", Network: []string{origin}, WAMResource: wam}
	b, _ := json.Marshal(m)
	return string(b)
}

const fixtureAudience = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"

const origin = "https://api.example.com"

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}
func setupSource(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write(t, filepath.Join(dir, "plugin.json"), manifest("example", "1", origin, ""))
	write(t, filepath.Join(dir, "index.js"), module)
	return dir
}
func active(t *testing.T, m *Manager) Entry {
	t.Helper()
	entries, err := m.List()
	if err != nil || len(entries) != 1 {
		t.Fatalf("active entries: %+v, %v", entries, err)
	}
	return entries[0]
}
func requireUnchanged(t *testing.T, m *Manager, want Entry) {
	t.Helper()
	if got := active(t, m); got != want {
		t.Fatalf("active entry changed: %+v, want %+v", got, want)
	}
	if _, err := plugin.Load(want.Dir); err != nil {
		t.Fatalf("last good version unavailable: %v", err)
	}
}
func TestLocalLifecycle(t *testing.T) {
	ctx := t.Context()
	dir := setupSource(t)
	write(t, filepath.Join(dir, ".git", "config"), "private metadata")
	root := t.TempDir()
	m := New(root)
	first, err := m.Install(ctx, dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	m = New(root)
	requireUnchanged(t, m, first)
	if _, err := os.Stat(filepath.Join(first.Dir, ".git")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("Git metadata copied")
	}
	if _, err = m.Install(ctx, setupSource(t), "", true); err == nil {
		t.Fatal("replaced another source")
	}
	requireUnchanged(t, m, first)
	write(t, filepath.Join(dir, "index.js"), "broken [")
	if _, err = m.Update(ctx, first.ID); err == nil {
		t.Fatal("activated broken plugin")
	}
	requireUnchanged(t, m, first)
	write(t, filepath.Join(dir, "index.js"), module)
	for _, change := range []string{manifest("example", "2", "https://other.example.com", ""), manifest("example", "2", origin, fixtureAudience), manifest("changed", "2", origin, "")} {
		write(t, filepath.Join(dir, "plugin.json"), change)
		if _, err = m.Update(ctx, first.ID); err == nil {
			t.Fatal("unapproved update")
		}
		requireUnchanged(t, m, first)
	}
	write(t, filepath.Join(dir, "plugin.json"), manifest("example", "2", origin, ""))
	second, err := m.Update(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if second.Revision == first.Revision || second.Dir == first.Dir || !second.Auto {
		t.Fatalf("expected new active version: %+v", second)
	}
	requireUnchanged(t, m, second)
	if _, err := os.Stat(first.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("retired version not removed")
	}
	if err = m.SetAuto(first.ID, false); err != nil {
		t.Fatal(err)
	}
	if active(t, m).Auto {
		t.Fatal("auto change not persisted")
	}
	if err = m.Remove(first.ID); err != nil {
		t.Fatal(err)
	}
	if entries, err := New(root).List(); err != nil || len(entries) != 0 {
		t.Fatalf("remove: %+v %v", entries, err)
	}
}

func TestReviewActivatesOnlyApprovedSnapshot(t *testing.T) {
	ctx := t.Context()
	dir := setupSource(t)
	root := t.TempDir()
	m := New(root)
	first, err := m.Install(ctx, dir, "", true)
	if err != nil {
		t.Fatal(err)
	}
	newOrigin := "https://new.example.com"
	write(t, filepath.Join(dir, "plugin.json"), manifest("example", "2", newOrigin, fixtureAudience))
	decline := errors.New("declined")
	_, err = m.InstallReviewed(ctx, dir, "", true, func(context.Context, plugin.Manifest) error { return decline })
	if !errors.Is(err, decline) {
		t.Fatalf("review error: %v", err)
	}
	requireUnchanged(t, m, first)
	_, err = m.InstallReviewed(ctx, dir, "", true, func(context.Context, plugin.Manifest) error { return New(root).SetAuto(first.ID, false) })
	if err == nil || active(t, m).Auto {
		t.Fatal("stale review overwrote changes")
	}
	second, err := m.InstallReviewed(ctx, dir, "", false, func(_ context.Context, p plugin.Manifest) error {
		if p.WAMResource != fixtureAudience || len(p.Network) != 1 || p.Network[0] != newOrigin {
			t.Fatalf("wrong review: %+v", p)
		}
		write(t, filepath.Join(dir, "plugin.json"), manifest("example", "3", origin, "cccccccc-cccc-4ccc-8ccc-cccccccccccc"))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	p, err := plugin.Load(second.Dir)
	if err != nil || p.Manifest.Version != "2" || p.Manifest.Network[0] != newOrigin {
		t.Fatalf("wrong reviewed snapshot: %+v, %v", p, err)
	}
	canceled, cancel := context.WithCancel(ctx)
	_, err = m.InstallReviewed(canceled, dir, "", false, func(context.Context, plugin.Manifest) error { cancel(); return nil })
	cancel()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	requireUnchanged(t, m, second)
}

type zipEntry struct {
	name string
	data string
	mode fs.FileMode
}

func packed(t *testing.T, files ...zipEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for _, f := range files {
		h := &zip.FileHeader{Name: f.name, Method: zip.Deflate}
		h.SetMode(f.mode)
		out, err := w.CreateHeader(h)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = out.Write([]byte(f.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func bundleEntries(prefix string) []zipEntry {
	return []zipEntry{{prefix + "plugin.json", manifest("example", "1", origin, ""), 0644}, {prefix + "index.js", module, 0755}}
}
func TestZIPValidationPreservesInstalledSnapshot(t *testing.T) {
	ctx := t.Context()
	zipPath := filepath.Join(t.TempDir(), "plugin.zip")
	m := New(t.TempDir())
	files := bundleEntries("bundle/")
	put := func(data []byte) {
		t.Helper()
		if err := os.WriteFile(zipPath, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	put(packed(t, append(files, zipEntry{"bundle/.GiT/config", "sensitive", 0600})...))
	first, err := m.Install(ctx, zipPath, "", false)
	if err != nil {
		t.Fatal(err)
	}
	requireUnchanged(t, m, first)
	if _, err = os.Stat(filepath.Join(first.Dir, ".GiT")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("included git metadata")
	}
	for _, extra := range []zipEntry{{"bundle/../../escape", "", 0600}, {`bundle/dir\escape`, "", 0600}, {"bundle/device:stream", "", 0600}, {"bundle/Link", "outside", fs.ModeSymlink | 0777}, {"bundle/index.js", "", 0600}, {"bundle/INDEX.js", "", 0600}, {"bundle/COn.txt", "", 0600}, {"bundle/space. ", "", 0600}, {"bundle/large", string(bytes.Repeat([]byte{'x'}, maxFile+1)), 0600}} {
		put(packed(t, append(files, extra)...))
		if _, err := m.Update(ctx, first.ID); err == nil {
			t.Fatalf("accepted %q", extra.name)
		}
		requireUnchanged(t, m, first)
	}
}
func TestLocalSymlinkRejected(t *testing.T) {
	dir := setupSource(t)
	target := filepath.Join(t.TempDir(), "private")
	write(t, target, "private")
	if err := os.Symlink(target, filepath.Join(dir, "resource")); err != nil {
		t.Skip(err)
	}
	if _, err := New(t.TempDir()).Install(t.Context(), dir, "", false); err == nil {
		t.Fatal("installed local symlink")
	}
}
