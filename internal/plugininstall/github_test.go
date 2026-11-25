package plugininstall

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
)

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Preserve the requested first-party host and path, but connect only to this
// test's TLS listener. Neither gh nor GitHub credentials are needed by the tests.
func serve(t *testing.T, m *Manager, h http.HandlerFunc) {
	t.Helper()
	s := httptest.NewTLSServer(h)
	t.Cleanup(s.Close)
	base, _ := url.Parse(s.URL)
	m.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		clone := r.Clone(r.Context())
		clone.Host = r.URL.Host
		clone.URL.Host = base.Host
		clone.URL.Scheme = base.Scheme
		return s.Client().Transport.RoundTrip(clone)
	})
}

const sha = "1111111111111111111111111111111111111111"
const treeSHA = "2222222222222222222222222222222222222222"
const commitResponse = `[{"sha":"` + sha + `","commit":{"tree":{"sha":"` + treeSHA + `"}}}]`

func TestPublicRepositoryRefAndIdentity(t *testing.T) {
	for _, ref := range []string{"", "release/next", "v1.0", sha} {
		t.Run("ref="+ref, func(t *testing.T) {
			m := New(t.TempDir())
			ctx := t.Context()
			archive := packed(t, bundleEntries("repo-"+sha+"/")...)
			var id atomic.Int64
			id.Store(12)
			var downloads atomic.Int32
			m.gh = func(context.Context, int64, ...string) ([]byte, error) {
				t.Fatal("public installation invoked gh")
				return nil, nil
			}
			serve(t, m, func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "" {
					t.Error("credentials in anonymous request")
				}
				switch r.Host + r.URL.Path {
				case "api.github.com/repos/owner/repo":
					fmt.Fprintf(w, `{"id":%d,"full_name":"Owner/Repo","default_branch":"primary"}`, id.Load())
				case "api.github.com/repos/owner/repo/commits":
					want := ref
					if want == "" {
						want = "primary"
					}
					if r.URL.Query().Get("sha") != want || r.URL.Query().Get("per_page") != "1" {
						t.Errorf("wrong ref query: %s", r.URL.RawQuery)
					}
					fmt.Fprint(w, commitResponse)
				case "codeload.github.com/owner/repo/zip/" + sha:
					downloads.Add(1)
					w.Write(archive)
				default:
					t.Errorf("unexpected GET %s%s", r.Host, r.URL)
					w.WriteHeader(404)
				}
			})
			e, err := m.Install(ctx, "https://GitHub.com/Owner/Repo.git", ref, true)
			if err != nil {
				t.Fatal(err)
			}
			if e.Source != "https://github.com/owner/repo" || e.Ref != ref || e.Revision != sha || e.RepoID != 12 {
				t.Fatalf("wrong source identity: %+v", e)
			}
			if unchanged, err := m.Update(ctx, e.ID); err != nil || unchanged != e || downloads.Load() != 1 {
				t.Fatalf("unchanged version fetched again: %+v %v", unchanged, err)
			}
			id.Store(21)
			if _, err := m.Update(ctx, e.ID); err == nil {
				t.Fatal("accepted new repo at old URL")
			}
			requireUnchanged(t, m, e)
		})
	}
}
func TestPrivateReposUseOnlyHostScopedAPI(t *testing.T) {
	for _, host := range []string{"github.com", "github.example.org"} {
		t.Run(host, func(t *testing.T) {
			m := New(t.TempDir())
			var anonymous atomic.Int32
			serve(t, m, func(w http.ResponseWriter, r *http.Request) {
				if host != "github.com" || r.Host != "api.github.com" || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected anonymous request: %s", r.Host)
				}
				anonymous.Add(1)
				w.WriteHeader(404)
			})
			const manifestSHA = "3333333333333333333333333333333333333333"
			const jsSHA = "4444444444444444444444444444444444444444"
			mf := manifest("example", "1", origin, "")
			logged := false
			m.gh = func(_ context.Context, _ int64, args ...string) ([]byte, error) {
				if slices.Equal(args, []string{"auth", "status", "--active", "--hostname", host}) {
					logged = true
					return nil, nil
				}
				if !logged || len(args) != 6 || !slices.Equal(args[:4], []string{"api", "--hostname", host, "-H"}) {
					return nil, fmt.Errorf("unexpected gh invocation: %q", args)
				}
				switch args[5] {
				case "repos/owner/repo":
					return []byte(`{"id":45,"full_name":"owner/repo","default_branch":"main"}`), nil
				case "repos/owner/repo/commits?per_page=1&sha=feature%2Fpermissions":
					return []byte(commitResponse), nil
				case "repos/owner/repo/git/trees/" + treeSHA + "?recursive=1":
					return []byte(fmt.Sprintf(`{"tree":[{"path":"index.js","type":"blob","mode":"100755","sha":%q,"size":%d,"url":"https://untrusted.invalid/blob"},{"path":"plugin.json","type":"blob","mode":"100644","sha":%q,"size":%d},{"path":".git/config","type":"blob","mode":"100644"}]}`, jsSHA, len(module), manifestSHA, len(mf))), nil
				case "repos/owner/repo/git/blobs/" + manifestSHA:
					return []byte(mf), nil
				case "repos/owner/repo/git/blobs/" + jsSHA:
					return []byte(module), nil
				}
				return nil, fmt.Errorf("unexpected endpoint: %s", args[5])
			}
			source := "owner/repo"
			if host != "github.com" {
				source = "https://" + host + "/owner/repo"
			}
			got, err := m.Install(t.Context(), source, "feature/permissions", true)
			if err != nil {
				t.Fatal(err)
			}
			if got.RepoID != 45 || got.Revision != sha {
				t.Fatalf("unexpected entry: %+v", got)
			}
			want := int32(0)
			if host == "github.com" {
				want = 1
			}
			if anonymous.Load() != want {
				t.Fatalf("anonymous calls: %d, want %d", anonymous.Load(), want)
			}
		})
	}
	env := ghEnv([]string{"GH_TOKEN=token", "GITHUB_TOKEN=token", "gh_enterprise_token=token", "GITHUB_ENTERPRISE_TOKEN=token", "PATH=bin"})
	if strings.Contains(strings.Join(env, "\n"), "=token") {
		t.Fatal("unscoped token in gh environment")
	}
}
func TestMovedRepositoryCannotRedirectInstall(t *testing.T) {
	m := New(t.TempDir())
	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "api.github.com" {
			t.Error("followed redirect")
		}
		http.Redirect(w, r, "https://other.invalid/fake", http.StatusMovedPermanently)
	})
	m.gh = func(context.Context, int64, ...string) ([]byte, error) {
		t.Fatal("followed redirect with gh")
		return nil, nil
	}
	if _, err := m.Install(t.Context(), "owner/repo", "", true); err == nil {
		t.Fatal("accepted redirect")
	}
}

func TestConcurrentRemoveWinsOverPendingUpdate(t *testing.T) {
	m := New(t.TempDir())
	m.gh = func(context.Context, int64, ...string) ([]byte, error) { return nil, fmt.Errorf("unexpected gh") }
	download := packed(t, bundleEntries("repo/")...)
	started, resume := make(chan struct{}), make(chan struct{})
	var updating atomic.Bool
	serve(t, m, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/repos/owner/repo":
			if updating.Load() {
				close(started)
				<-resume
			}
			json.NewEncoder(w).Encode(map[string]any{"id": 7, "full_name": "owner/repo", "default_branch": "main"})
		case r.URL.Path == "/repos/owner/repo/commits":
			fmt.Fprint(w, commitResponse)
		default:
			w.Write(download)
		}
	})
	e, err := m.Install(t.Context(), "owner/repo", "", true)
	if err != nil {
		t.Fatal(err)
	}
	updating.Store(true)
	result := make(chan error, 1)
	go func() { _, err := m.Update(t.Context(), e.ID); result <- err }()
	<-started
	err = New(m.root).Remove(e.ID)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("stale update succeeded after removal")
	}
	if entries, err := m.List(); err != nil || len(entries) != 0 {
		t.Fatalf("removed entry resurrected: %+v, %v", entries, err)
	}
}
