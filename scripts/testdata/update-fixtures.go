//go:build ignore

// Generate isolated signed test archives. This creates a fresh ephemeral key,
// writes only its public half, and never reads production keys or credentials.
package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

func main() {
	dir := os.Args[1]
	public, private, err := ed25519.GenerateKey(rand.Reader)
	check(err)
	check(os.WriteFile(filepath.Join(dir, "test-public-key.txt"), []byte(base64.StdEncoding.EncodeToString(public)), 0o600))
	for _, target := range []string{"windows-amd64", "windows-arm64", "darwin-amd64", "darwin-arm64"} {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		prefix, executable := "", "Copilot Chat.exe"
		if strings.HasPrefix(target, "darwin-") {
			prefix = "Copilot Chat.app/Contents/Resources/"
			executable = "Copilot Chat.app/Contents/MacOS/Copilot Chat"
		}
		for _, name := range []string{executable, prefix + "README.md", prefix + "licenses/test/LICENSE"} {
			content := []byte("isolated signed fixture for " + name)
			check(tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}))
			_, err := tw.Write(content)
			check(err)
		}
		check(tw.Close())
		check(gz.Close())
		archive := "copilot-chat-0.3.0-" + target + ".tar.gz"
		check(os.WriteFile(filepath.Join(dir, archive), buf.Bytes(), 0o600))
		hash := sha256.Sum256(buf.Bytes())
		manifest, err := json.Marshal(map[string]any{
			"version": "0.3.0", "url": "https://github.com/test-owner/test-repo/releases/download/v0.3.0/" + archive,
			"size": buf.Len(), "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(private, hash[:])),
		})
		check(err)
		check(os.WriteFile(filepath.Join(dir, "update-"+target+".json"), manifest, 0o600))
	}
}

func check(err error) {
	if err != nil {
		panic(err)
	}
}
