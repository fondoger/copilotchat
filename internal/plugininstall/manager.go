// Package plugininstall manages validated, immutable plugin snapshots. The index
// is the only activation point: neither copying files nor compiling a plugin
// makes it visible to callers.
package plugininstall

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/plugin"
)

type Entry struct {
	ID, Name, Dir, Source, Ref, Revision string
	Auto                                 bool
	RepoID                               int64 // GitHub's stable repository identity; zero for local sources.
}

type installed struct {
	Entry       // Dir is a generated basename on disk, an absolute path in the public API.
	Network     []string
	WAMResource string
}

type Manager struct {
	root string
	http *http.Client
	gh   func(context.Context, int64, ...string) ([]byte, error)
}

func New(root string) *Manager {
	if abs, err := filepath.Abs(root); err == nil {
		root = abs
	}
	return &Manager{root: root, gh: runGH, http: &http.Client{
		Timeout: 45 * time.Second,
		// A moved repository is a different source. Archive storage is addressed
		// directly and anonymously; API redirects are never required.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

var versionName = regexp.MustCompile(`^version-[A-Z2-7]{26,}$`)

// Lock only the short read/commit transaction, not network or compilation. The
// OS releases this lock on process exit, including crashes during installation.
func (m *Manager) withIndex(fn func(map[string]installed) error) error {
	if err := os.MkdirAll(m.root, 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(m.root, "index.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := lockFile(f); err != nil {
		return err
	}
	defer unlockFile(f)
	index := make(map[string]installed)
	b, err := os.ReadFile(filepath.Join(m.root, "index.json"))
	if err == nil {
		if err := json.Unmarshal(b, &index); err != nil {
			return err
		}
		if index == nil {
			return errors.New("invalid plugin index")
		}
		for id, r := range index {
			if id == "" || id != r.ID || !versionName.MatchString(r.Dir) {
				return errors.New("invalid plugin index entry")
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return fn(index)
}

func (m *Manager) save(index map[string]installed) error {
	f, err := os.CreateTemp(m.root, ".index-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err = json.NewEncoder(f).Encode(index); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), filepath.Join(m.root, "index.json"))
}

func (m *Manager) entry(r installed) Entry {
	e := r.Entry
	e.Dir = filepath.Join(m.root, e.Dir)
	return e
}

func (m *Manager) List() ([]Entry, error) {
	var out []Entry
	err := m.withIndex(func(index map[string]installed) error {
		for _, r := range index {
			out = append(out, m.entry(r))
		}
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, err
}

func (m *Manager) SetAuto(id string, on bool) error {
	return m.withIndex(func(index map[string]installed) error {
		r, ok := index[id]
		if !ok {
			return fs.ErrNotExist
		}
		if r.Auto == on {
			return nil
		}
		r.Auto = on
		index[id] = r
		return m.save(index)
	})
}

func (m *Manager) Remove(id string) error {
	var dir string
	err := m.withIndex(func(index map[string]installed) error {
		r, ok := index[id]
		if !ok {
			return fs.ErrNotExist
		}
		delete(index, id)
		dir = m.entry(r).Dir
		return m.save(index)
	})
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	return err
}

func (m *Manager) Install(ctx context.Context, source, ref string, auto bool) (Entry, error) {
	return m.install(ctx, source, ref, auto, nil, nil)
}

// InstallReviewed activates exactly the staged bundle the user reviewed. The
// callback runs outside the index transaction and may decline the installation.
func (m *Manager) InstallReviewed(ctx context.Context, source, ref string, auto bool, review func(context.Context, plugin.Manifest) error) (Entry, error) {
	if review == nil {
		return Entry{}, errors.New("missing plugin review callback")
	}
	return m.install(ctx, source, ref, auto, nil, review)
}

// Update follows the installed source/ref only. New permissions require an
// explicit Install, whether or not the user has enabled scheduled updates.
func (m *Manager) Update(ctx context.Context, id string) (Entry, error) {
	var old installed
	err := m.withIndex(func(index map[string]installed) error {
		var ok bool
		old, ok = index[id]
		if !ok {
			return fs.ErrNotExist
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	return m.install(ctx, old.Source, old.Ref, old.Auto, &old, nil)
}

func (m *Manager) install(ctx context.Context, source, ref string, auto bool, expected *installed, review func(context.Context, plugin.Manifest) error) (Entry, error) {
	if err := ctx.Err(); err != nil {
		return Entry{}, err
	}
	s, err := parseSource(source)
	if err != nil {
		return Entry{}, err
	}
	if expected != nil && expected.Source != s.name {
		return Entry{}, errors.New("plugin source changed")
	}
	if s.repo == nil && ref != "" {
		return Entry{}, errors.New("refs apply only to GitHub repositories")
	}
	if err = os.MkdirAll(m.root, 0700); err != nil {
		return Entry{}, err
	}
	if s.repo == nil {
		root, err := filepath.EvalSymlinks(m.root)
		if err != nil {
			return Entry{}, err
		}
		if containsPath(s.name, root) || containsPath(root, s.name) {
			return Entry{}, errors.New("local source overlaps plugin storage")
		}
	}
	stage, err := os.MkdirTemp(m.root, ".stage-*")
	if err != nil {
		return Entry{}, err
	}
	defer os.RemoveAll(stage)
	dir := filepath.Join(stage, "bundle")
	if err = os.Mkdir(dir, 0700); err != nil {
		return Entry{}, err
	}
	var revision string
	var repoID int64
	if s.repo != nil {
		repoID, revision, err = m.fetchRepo(ctx, *s.repo, ref, dir, expected)
	} else {
		err = copyLocal(ctx, s.name, dir)
	}
	if err != nil {
		return Entry{}, err
	}
	unchanged := expected != nil && s.repo != nil && expected.Revision == revision
	var r, reviewed installed
	if unchanged {
		r = *expected
	} else {
		if dir, err = pluginRoot(dir); err != nil {
			return Entry{}, err
		}
		p, err := plugin.Load(dir)
		if err != nil {
			return Entry{}, err
		}
		manifest := p.Manifest
		if revision == "" {
			revision, err = contentRevision(ctx, dir)
			if err != nil {
				return Entry{}, err
			}
		}
		network := slices.Clone(manifest.Network)
		slices.Sort(network)
		network = slices.Compact(network)
		r = installed{Entry: Entry{ID: manifest.ID, Name: manifest.Name, Dir: "version-" + rand.Text(), Source: s.name, Ref: ref, Revision: revision, Auto: auto, RepoID: repoID}, Network: network, WAMResource: manifest.WAMResource}
		if expected != nil && (expected.ID != r.ID || expected.WAMResource != r.WAMResource || !slices.Equal(expected.Network, r.Network)) {
			return Entry{}, errors.New("plugin identity or permissions changed; explicit installation required")
		}
		if review != nil {
			err = m.withIndex(func(index map[string]installed) error {
				reviewed = index[r.ID]
				if reviewed.ID != "" && (reviewed.Source != r.Source || reviewed.RepoID != r.RepoID) {
					return fmt.Errorf("plugin %q already installed from another source", r.ID)
				}
				return nil
			})
			if err != nil {
				return Entry{}, err
			}
			if err = ctx.Err(); err != nil {
				return Entry{}, err
			}
			if err = review(ctx, manifest); err != nil {
				return Entry{}, err
			}
		}
	}
	var retired string
	err = m.withIndex(func(index map[string]installed) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		old, ok := index[r.ID]
		if review != nil && !reflect.DeepEqual(old, reviewed) {
			return errors.New("plugin changed during review")
		}
		if expected != nil && (!ok || !reflect.DeepEqual(old, *expected)) {
			return errors.New("plugin changed during update")
		}
		if ok && (old.Source != r.Source || old.RepoID != r.RepoID) {
			return fmt.Errorf("plugin %q already installed from another source", r.ID)
		}
		if expected != nil && old.Revision == r.Revision {
			r = old
			return nil
		}
		target := m.entry(r).Dir
		if err := os.Rename(dir, target); err != nil {
			return err
		}
		index[r.ID] = r
		if err := m.save(index); err != nil {
			_ = os.RemoveAll(target)
			return err
		}
		if ok {
			retired = m.entry(old).Dir
		}
		return nil
	})
	if err != nil {
		return Entry{}, err
	}
	if retired != "" {
		_ = os.RemoveAll(retired)
	}
	return m.entry(r), nil
}

func containsPath(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && (rel == "." || filepath.IsLocal(rel))
}

func pluginRoot(dir string) (string, error) {
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err == nil {
		return dir, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	if len(entries) == 1 && entries[0].IsDir() {
		return filepath.Join(dir, entries[0].Name()), nil
	}
	return dir, nil
}

// Hash a sorted stream of (path, content digest) pairs. ZIP timestamps, wrapper
// folders and the source filesystem's enumeration order do not change a release.
func contentRevision(ctx context.Context, dir string) (string, error) {
	h := sha256.New()
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		defer f.Close()
		digest := sha256.New()
		if _, err = io.Copy(digest, f); err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		h.Write(digest.Sum(nil))
		return nil
	})
	return hex.EncodeToString(h.Sum(nil)), err
}

type sourceSpec struct {
	name string
	repo *repository
}

func parseSource(source string) (sourceSpec, error) {
	source = strings.TrimSpace(source)
	if source == "" {
		return sourceSpec{}, errors.New("missing plugin source")
	}
	if !strings.Contains(source, "://") {
		if strings.HasPrefix(strings.ReplaceAll(source, `\`, "/"), "//") {
			return sourceSpec{}, errors.New("expected a local path, not a network share or device path")
		}
		path := filepath.Clean(source)
		st, err := os.Lstat(path)
		if err == nil {
			if !st.IsDir() && !st.Mode().IsRegular() {
				return sourceSpec{}, errors.New("local source must be a directory or regular ZIP file")
			}
			path, err = filepath.Abs(path)
			if err != nil {
				return sourceSpec{}, err
			}
			path, err = filepath.EvalSymlinks(path)
			return sourceSpec{name: path}, err
		}
		if !errors.Is(err, fs.ErrNotExist) || filepath.IsAbs(source) || strings.HasPrefix(source, ".") {
			return sourceSpec{}, err
		}
	}
	r, err := parseRepository(source)
	if err != nil {
		return sourceSpec{}, err
	}
	return sourceSpec{name: "https://" + r.host + "/" + r.slug, repo: &r}, nil
}
