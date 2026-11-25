// Package plugin runs provider policy in a bounded MoeJS runtime. Go owns HTTP,
// WAM and streaming; a module never receives a credential or a request body.
package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/MoeForge/moejs"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/wam"
)

type Manifest struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Version     string   `json:"version"`
	Main        string   `json:"main"`
	Network     []string `json:"network"`
	WAMClientID string   `json:"wamClientId,omitempty"`
	WAMResource string   `json:"wamResource,omitempty"`
}

type Plugin struct {
	Manifest Manifest
	mod      *moejs.Module
}

var identifier = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
var hostname = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]*[a-z0-9])?\.)*[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

func Load(dir string) (*Plugin, error) {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	read := func(name string, max int64) ([]byte, error) {
		if !fs.ValidPath(name) || !filepath.IsLocal(name) || strings.ContainsAny(name, `\:`) {
			return nil, errors.New("expected a relative plugin file")
		}
		f, err := root.Open(name)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		st, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if !st.Mode().IsRegular() || st.Size() > max {
			return nil, fmt.Errorf("invalid or oversized plugin file: %s", name)
		}
		b, err := io.ReadAll(io.LimitReader(f, max+1))
		if int64(len(b)) > max {
			return nil, fmt.Errorf("plugin file too large: %s", name)
		}
		return b, err
	}
	b, err := read("plugin.json", 64<<10)
	if err != nil {
		return nil, err
	}
	var m Manifest
	decoder := json.NewDecoder(bytes.NewReader(b))
	// A misspelled authentication permission must not produce an anonymous provider.
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugin.json: %w", err)
	}
	if err = decoder.Decode(new(json.RawMessage)); err != io.EOF {
		return nil, errors.New("plugin.json must contain a single manifest")
	}
	if !identifier.MatchString(m.ID) || !filepath.IsLocal(m.ID) || strings.HasSuffix(m.ID, ".") || strings.TrimSpace(m.Name) == "" || strings.TrimSpace(m.Version) == "" {
		return nil, errors.New("plugin.json needs a safe lowercase id, name and version")
	}
	if (m.WAMClientID != "" || m.WAMResource != "") && (!wam.ValidID(m.WAMClientID) || !wam.ValidID(m.WAMResource)) {
		return nil, errors.New("wamClientId and wamResource must both be nonzero GUIDs")
	}
	m.WAMClientID = strings.ToLower(m.WAMClientID)
	m.WAMResource = strings.ToLower(m.WAMResource)
	if len(m.Network) == 0 {
		return nil, errors.New("declare at least one HTTPS network origin")
	}
	for _, origin := range m.Network {
		u, err := url.Parse(origin)
		if err != nil || u.Scheme != "https" || u.User != nil || !hostname.MatchString(u.Host) || origin != "https://"+u.Host {
			return nil, fmt.Errorf("invalid HTTPS origin %q", origin)
		}
	}
	if m.Main == "" {
		m.Main = "index.js"
	}
	source, err := read(m.Main, 1<<20)
	if err != nil {
		return nil, err
	}
	compile := moejs.Compile
	switch filepath.Ext(m.Main) {
	case ".ts":
		compile = moejs.CompileTS
	case ".js", ".mjs":
	default:
		return nil, errors.New("plugin entry must be JavaScript or TypeScript")
	}
	mod, err := compile(m.Main, string(source))
	if err != nil {
		return nil, err
	}
	if len(mod.Requests()) != 0 {
		return nil, errors.New("bundle the plugin as one module before installing")
	}
	required := []string{"catalog", "models", "inference"}
	if m.WAMResource != "" {
		required = append(required, "signIn")
	}
	for _, name := range required {
		if !slices.Contains(mod.Exports(), name) {
			return nil, fmt.Errorf("missing plugin export %q", name)
		}
	}
	return &Plugin{Manifest: m, mod: mod}, nil
}

// Each call has private globals, even when two accounts share an installation.
// Only compiled bytecode is shared. The deadline also covers top-level code.
func (p *Plugin) withRuntime(ctx context.Context, invoke func(*moejs.Runtime, moejs.Value) error) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	rt := moejs.NewRuntime(moejs.Options{MemoryLimit: 64 << 20, DisableDynamicCode: true})
	stop := context.AfterFunc(ctx, func() { rt.Interrupt(context.Canceled) })
	defer stop()
	model := rt.Function("model", 2, func(_ *moejs.Realm, _ moejs.Value, args []moejs.Value) (moejs.Value, error) {
		var entry provider.CatalogEntry
		b, err := rt.AppendJSON(nil, moejs.Arg(args, 0))
		if err != nil {
			return moejs.Undefined(), err
		}
		if err = json.Unmarshal(b, &entry); err != nil {
			return moejs.Undefined(), err
		}
		endpoint := moejs.Arg(args, 1)
		if !endpoint.IsString() {
			return moejs.Undefined(), errors.New("model endpoint must be a string")
		}
		return rt.FromGo(entry.Model(endpoint.String()))
	})
	// Expose the host contract version for plugin authors.
	host, err := rt.FromGo(map[string]any{"apiVersion": 3, "model": model})
	if err != nil {
		return err
	}
	if err = rt.SetGlobal("host", host); err != nil {
		return err
	}
	if err = rt.Load(p.mod); err != nil {
		return err
	}
	return invoke(rt, host)
}

func (p *Plugin) call(ctx context.Context, name string, params, out any) error {
	return p.withRuntime(ctx, func(rt *moejs.Runtime, _ moejs.Value) error {
		arg, err := rt.FromGo(params)
		if err != nil {
			return err
		}
		fn, _ := rt.Export(name)
		res, err := rt.CallFunction(fn, moejs.Undefined(), arg)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", p.Manifest.ID, name, err)
		}
		b, err := rt.AppendJSONWith(nil, res, moejs.JSONOptions{Strict: true})
		if err != nil {
			return err
		}
		return json.Unmarshal(b, out)
	})
}
