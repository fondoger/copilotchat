// verify-update checks the signed assets emitted by MyGo before publishing.
package main

import (
	"archive/tar"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

type manifest struct {
	Version   string            `json:"version"`
	URL       string            `json:"url"`
	Size      int64             `json:"size"`
	Signature string            `json:"signature"`
	Deltas    []json.RawMessage `json:"deltas"`
}

type options struct {
	archive, manifest, version, url, publicKey, appName, target string
	requireHelper                                               bool
}

func main() {
	var o options
	flag.StringVar(&o.archive, "archive", "", "MyGo tar.gz archive")
	flag.StringVar(&o.manifest, "manifest", "", "MyGo update manifest")
	flag.StringVar(&o.version, "version", "", "expected release version")
	flag.StringVar(&o.url, "url", "", "expected immutable archive URL")
	flag.StringVar(&o.publicKey, "public-key", "", "base64 Ed25519 public key")
	flag.StringVar(&o.appName, "app-name", "", "application name")
	flag.StringVar(&o.target, "target", "", "MyGo target")
	flag.BoolVar(&o.requireHelper, "require-helper", false, "require the bundled credential helper")
	flag.Parse()
	if err := verify(o); err != nil {
		fmt.Fprintln(os.Stderr, "Invalid signed update:", err)
		os.Exit(1)
	}
}

func verify(o options) error {
	if o.archive == "" || o.manifest == "" || o.version == "" || o.url == "" || o.appName == "" {
		return errors.New("archive, manifest, version, url and app-name are required")
	}
	if o.target != "windows-amd64" && o.target != "windows-arm64" && o.target != "darwin-amd64" && o.target != "darwin-arm64" {
		return errors.New("unsupported target")
	}
	data, err := os.ReadFile(o.manifest)
	if err != nil {
		return err
	}
	var m manifest
	if len(data) > 1<<20 {
		return errors.New("manifest exceeds the updater limit")
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return err
	}
	if m.Version != o.version || m.URL != o.url {
		return errors.New("manifest version or archive URL differs from the release")
	}
	if len(m.Deltas) != 0 {
		return errors.New("delta assets are not supported by this release pipeline")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(o.publicKey))
	if err != nil || len(key) != ed25519.PublicKeySize {
		return errors.New("invalid public key")
	}
	signature, err := base64.StdEncoding.DecodeString(m.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid Ed25519 signature")
	}
	f, err := os.Open(o.archive)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if m.Size <= 0 || m.Size > 1<<30 || m.Size != info.Size() {
		return errors.New("manifest size differs from the archive or exceeds the updater limit")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, f); err != nil {
		return err
	}
	if !ed25519.Verify(ed25519.PublicKey(key), hash.Sum(nil), signature) {
		return errors.New("archive signature does not match the configured public key")
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	return verifyArchive(f, o.appName, o.target, o.requireHelper)
}

func verifyArchive(r io.Reader, appName, target string, requireHelper bool) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	prefix, executable := "", appName+".exe"
	mac := strings.HasPrefix(target, "darwin-")
	if mac {
		prefix = appName + ".app/Contents/Resources/"
		executable = appName + ".app/Contents/MacOS/" + appName
	}
	want := map[string]bool{executable: false, prefix + "README.md": false, prefix + "THIRD_PARTY_NOTICES.md": false}
	helper := prefix + "copilot-chat-api-key-helper"
	if !mac {
		helper += ".exe"
	}
	if requireHelper {
		want[helper] = false
	}
	licenses := false
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		name := strings.TrimSuffix(h.Name, "/")
		if path.IsAbs(name) || path.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") || strings.Contains(name, "\\") {
			return fmt.Errorf("unsafe archive entry %q", h.Name)
		}
		if mac && name != appName+".app" && !strings.HasPrefix(name, appName+".app/") {
			return errors.New("macOS update must contain exactly the application bundle")
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if _, ok := want[name]; ok {
			if h.Size <= 0 {
				return fmt.Errorf("empty required archive file %q", name)
			}
			want[name] = true
		}
		if (name == executable || (requireHelper && name == helper)) && mac && h.Mode&0o111 == 0 {
			return errors.New("macOS update executable has no execute permission")
		}
		if strings.HasPrefix(name, prefix+"licenses/") && h.Size > 0 {
			licenses = true
		}
	}
	for name, found := range want {
		if !found {
			return fmt.Errorf("update archive is missing %s", name)
		}
	}
	if !licenses {
		return errors.New("update archive is missing third-party licenses")
	}
	return nil
}
