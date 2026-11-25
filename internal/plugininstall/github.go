package plugininstall

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type repository struct{ host, slug string }

var repoPart = regexp.MustCompile(`^[a-zA-Z0-9._-]{1,100}$`)
var dnsLabel = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
var objectID = regexp.MustCompile(`^[0-9a-f]{40}$`)

func parseRepository(source string) (repository, error) {
	host, slug := "github.com", source
	if strings.Contains(source, "://") {
		u, err := url.Parse(source)
		if err != nil || u.Scheme != "https" || u.User != nil || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" {
			return repository{}, errors.New("expected an HTTPS GitHub repository URL")
		}
		host = strings.ToLower(u.Host)
		slug = strings.TrimPrefix(u.Path, "/")
		if len(host) > 253 {
			return repository{}, errors.New("invalid repository host")
		}
		for _, label := range strings.Split(host, ".") {
			if !dnsLabel.MatchString(label) {
				return repository{}, errors.New("invalid repository host")
			}
		}
	}
	slug = strings.TrimSuffix(strings.TrimSuffix(slug, "/"), ".git")
	parts := strings.Split(slug, "/")
	if len(parts) != 2 {
		return repository{}, errors.New("expected GitHub owner/repo")
	}
	for _, p := range parts {
		if !repoPart.MatchString(p) || p == "." || p == ".." {
			return repository{}, errors.New("invalid GitHub owner/repo")
		}
	}
	return repository{host, strings.ToLower(slug)}, nil
}

// An install never obtains a token. Public requests have no Authorization
// header. Private requests use only constructed, host-relative JSON/blob API
// routes; no archive redirect, download_url, or server-supplied URL goes to gh.
type repoSession struct {
	m             *Manager
	r             repository
	authenticated bool
}

func (s *repoSession) login(ctx context.Context) error {
	_, err := s.m.gh(ctx, 64<<10, "auth", "status", "--active", "--hostname", s.r.host)
	if err != nil {
		return fmt.Errorf("a gh login for %s is required: %w", s.r.host, err)
	}
	s.authenticated = true
	return nil
}
func (s *repoSession) api(ctx context.Context, route, accept string, limit int64) ([]byte, error) {
	if !s.authenticated && s.r.host == "github.com" {
		b, status, err := s.m.anonymous(ctx, "https://api.github.com/"+route, accept, limit)
		if err == nil || !authStatus(status) {
			return b, err
		}
	}
	if !s.authenticated {
		if err := s.login(ctx); err != nil {
			return nil, err
		}
	}
	return s.m.gh(ctx, limit, "api", "--hostname", s.r.host, "-H", "Accept: "+accept, route)
}
func authStatus(n int) bool { return n == 401 || n == 403 || n == 404 || n == 429 }
func (m *Manager) anonymous(ctx context.Context, endpoint, accept string, limit int64) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Accept", accept)
	res, err := m.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, res.StatusCode, fmt.Errorf("GitHub returned HTTP %d", res.StatusCode)
	}
	if res.ContentLength > limit {
		return nil, res.StatusCode, errors.New("GitHub response exceeds size limit")
	}
	b, err := readLimit(res.Body, limit)
	return b, res.StatusCode, err
}
func readLimit(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if int64(len(b)) > n {
		return nil, errors.New("GitHub response exceeds size limit")
	}
	return b, err
}

func (s *repoSession) json(ctx context.Context, route string, dest any, limit int64) error {
	b, err := s.api(ctx, route, "application/vnd.github+json", limit)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, dest)
}

func (m *Manager) fetchRepo(ctx context.Context, r repository, ref, dir string, old *installed) (int64, string, error) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	s := repoSession{m: m, r: r}
	route := "repos/" + r.slug
	var meta struct {
		ID       int64
		FullName string `json:"full_name"`
		Default  string `json:"default_branch"`
	}
	if err := s.json(ctx, route, &meta, 256<<10); err != nil {
		return 0, "", err
	}
	if meta.ID <= 0 || !strings.EqualFold(meta.FullName, r.slug) || old != nil && old.RepoID != meta.ID {
		return 0, "", errors.New("GitHub repository identity changed")
	}
	if ref == "" {
		ref = meta.Default
	}
	if ref == "" || len(ref) > 1024 {
		return 0, "", errors.New("invalid GitHub ref")
	}
	var commits []struct {
		SHA    string
		Commit struct{ Tree struct{ SHA string } }
	}
	if err := s.json(ctx, route+"/commits?per_page=1&sha="+url.QueryEscape(ref), &commits, 1<<20); err != nil {
		return 0, "", err
	}
	if len(commits) != 1 || !objectID.MatchString(commits[0].SHA) || !objectID.MatchString(commits[0].Commit.Tree.SHA) {
		return 0, "", errors.New("invalid GitHub commit")
	}
	sha := commits[0].SHA
	if old != nil && old.Revision == sha {
		return meta.ID, sha, nil
	}
	b := newBundle(ctx, dir)
	if !s.authenticated {
		data, status, err := m.anonymous(ctx, "https://codeload.github.com/"+r.slug+"/zip/"+sha, "application/zip", maxArchive)
		if err == nil {
			return meta.ID, sha, b.unzip(bytes.NewReader(data), int64(len(data)))
		}
		if !authStatus(status) {
			return 0, "", err
		}
		if err = s.login(ctx); err != nil {
			return 0, "", err
		}
	}
	// Private GitHub archives redirect to a signed download URL. Instead, fetch
	// immutable tree and blob objects through the authenticated API itself.
	var tree struct {
		Truncated bool
		Tree      []struct {
			Path, Mode, Type, SHA string
			Size                  int64
		}
	}
	if err := s.json(ctx, route+"/git/trees/"+commits[0].Commit.Tree.SHA+"?recursive=1", &tree, 3<<20); err != nil {
		return 0, "", err
	}
	if tree.Truncated || len(tree.Tree) > maxEntries {
		return 0, "", errors.New("incomplete or oversized GitHub tree")
	}
	for _, node := range tree.Tree {
		directory := node.Type == "tree" && node.Mode == "040000"
		keep, err := b.reserve(node.Path, node.Size, directory)
		if err != nil {
			return 0, "", err
		}
		if !keep {
			continue
		}
		if directory {
			if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(node.Path)), 0700); err != nil {
				return 0, "", err
			}
			continue
		}
		if node.Type != "blob" || (node.Mode != "100644" && node.Mode != "100755") || !objectID.MatchString(node.SHA) {
			return 0, "", fmt.Errorf("unsupported GitHub tree entry %q", node.Path)
		}
		data, err := s.api(ctx, route+"/git/blobs/"+node.SHA, "application/vnd.github.raw+json", node.Size)
		if err != nil {
			return 0, "", err
		}
		if err = b.write(node.Path, bytes.NewReader(data), node.Size); err != nil {
			return 0, "", err
		}
	}
	return meta.ID, sha, nil
}

// Enterprise environment tokens are not host-scoped. Use credentials registered
// for the exact hostname by gh auth login. In particular, a pasted HTTPS URL
// can never direct GH_ENTERPRISE_TOKEN (or a github.com token) to a new host.
func ghEnv(env []string) []string {
	var out []string
	for _, v := range env {
		k, _, _ := strings.Cut(v, "=")
		switch strings.ToUpper(k) {
		case "GH_TOKEN", "GITHUB_TOKEN", "GH_ENTERPRISE_TOKEN", "GITHUB_ENTERPRISE_TOKEN", "GH_HOST", "GH_REPO", "GH_DEBUG", "GH_PROMPT_DISABLED", "GH_NO_UPDATE_NOTIFIER", "GH_NO_EXTENSION_UPDATE_NOTIFIER", "GH_PAGER", "CLICOLOR_FORCE", "GH_FORCE_TTY":
			continue
		}
		out = append(out, v)
	}
	return append(out, "GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "GH_NO_EXTENSION_UPDATE_NOTIFIER=1", "GH_PAGER=")
}
func runGH(ctx context.Context, limit int64, args ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", args...)
	// The caller's CWD may itself be the untrusted repository. gh should not
	// inspect repository-local configuration when selecting its credentials.
	app, err := os.Executable()
	if err != nil {
		return nil, err
	}
	cmd.Dir = filepath.Dir(app)
	prepareCLI(cmd)
	cmd.Env = ghEnv(os.Environ())
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
	}
	b, err := readLimit(out, limit)
	if err != nil {
		cancel()
	}
	waitErr := cmd.Wait()
	if err != nil {
		return nil, err
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if waitErr != nil {
		return nil, fmt.Errorf("GitHub CLI: %w", waitErr)
	}
	return b, nil
}
