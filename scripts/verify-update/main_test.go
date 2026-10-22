package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func signedFixture(t *testing.T, target string) (options, manifest) {
	t.Helper()
	dir := t.TempDir()
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	prefix, executable := "", "Copilot Chat.exe"
	if strings.HasPrefix(target, "darwin-") {
		prefix = "Copilot Chat.app/Contents/Resources/"
		executable = "Copilot Chat.app/Contents/MacOS/Copilot Chat"
	}
	for _, name := range []string{executable, prefix + "README.md", prefix + "licenses/test/LICENSE"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: 7, Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte("fixture")); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	o := options{archive: filepath.Join(dir, "update.tar.gz"), manifest: filepath.Join(dir, "update.json"),
		version: "0.3.1", url: "https://github.com/example/app/releases/download/v0.3.1/update.tar.gz",
		publicKey: base64.StdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)), appName: "Copilot Chat", target: target}
	if err := os.WriteFile(o.archive, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(b.Bytes())
	m := manifest{Version: o.version, URL: o.url, Size: int64(b.Len()), Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(key, hash[:]))}
	writeManifest(t, o, m)
	return o, m
}

func writeManifest(t *testing.T, o options, m manifest) {
	t.Helper()
	data, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(o.manifest, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestSignedPlatforms(t *testing.T) {
	for _, target := range []string{"windows-amd64", "windows-arm64", "darwin-amd64", "darwin-arm64"} {
		t.Run(target, func(t *testing.T) {
			o, _ := signedFixture(t, target)
			if err := verify(o); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRejectAlteredSignedUpdates(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*options, *manifest)
	}{
		{"version", func(_ *options, m *manifest) { m.Version = "9.9.9" }},
		{"url", func(_ *options, m *manifest) { m.URL += "?other-release" }},
		{"size", func(_ *options, m *manifest) { m.Size++ }},
		{"signature", func(_ *options, m *manifest) { m.Signature = base64.StdEncoding.EncodeToString(make([]byte, 64)) }},
		{"key", func(o *options, _ *manifest) { o.publicKey = base64.StdEncoding.EncodeToString(make([]byte, 32)) }},
		{"delta", func(_ *options, m *manifest) { m.Deltas = []json.RawMessage{json.RawMessage(`{"from":"0.3.0"}`)} }},
		{"layout", func(o *options, _ *manifest) { o.target = "darwin-arm64" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			o, m := signedFixture(t, "windows-amd64")
			test.edit(&o, &m)
			writeManifest(t, o, m)
			if err := verify(o); err == nil {
				t.Fatal("altered update was accepted")
			}
		})
	}
	t.Run("archive bytes", func(t *testing.T) {
		o, _ := signedFixture(t, "windows-amd64")
		b, err := os.ReadFile(o.archive)
		if err != nil {
			t.Fatal(err)
		}
		b[len(b)/2] ^= 1
		if err := os.WriteFile(o.archive, b, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := verify(o); err == nil {
			t.Fatal("tampered archive was accepted")
		}
	})
}
