package credentials

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/egoist/mygo"
)

type vaultManifest struct {
	Generation string       `json:"generation"`
	Chunks     int          `json:"chunks"`
	SHA256     string       `json:"sha256"`
	Retired    []generation `json:"retired,omitempty"`
}

type generation struct {
	Generation string `json:"generation"`
	Chunks     int    `json:"chunks"`
}

var errInvalidManifest = errors.New("invalid saved plugin sign-in; reconnect this provider")

func validSecretGeneration(generation string, chunks int) bool {
	decoded, err := hex.DecodeString(generation)
	return err == nil && len(decoded) == 12 && chunks >= 1 && chunks <= 128
}

// PluginKey scopes the vault entry to an installation identity and one configured account.
func PluginKey(pluginID, instanceID string) string {
	data, _ := json.Marshal([2]string{pluginID, instanceID})
	return fmt.Sprintf("plugin-sign-in:%x", sha256.Sum256(data))
}
func chunkKey(key string, manifest vaultManifest, index int) string {
	return fmt.Sprintf("%s:%s:%d", key, manifest.Generation, index)
}

func readManifest(secrets Store, key string) (vaultManifest, error) {
	data, err := secrets.Get(key)
	if err != nil {
		return vaultManifest{}, err
	}
	var manifest vaultManifest
	if json.Unmarshal(data, &manifest) != nil || !validSecretGeneration(manifest.Generation, manifest.Chunks) || len(manifest.Retired) > 16 {
		return vaultManifest{}, errInvalidManifest
	}
	if checksum, err := hex.DecodeString(manifest.SHA256); err != nil || len(checksum) != 32 {
		return vaultManifest{}, errInvalidManifest
	}
	seen := map[string]bool{manifest.Generation: true}
	for _, retired := range manifest.Retired {
		if !validSecretGeneration(retired.Generation, retired.Chunks) || seen[retired.Generation] {
			return vaultManifest{}, errInvalidManifest
		}
		seen[retired.Generation] = true
	}
	return manifest, nil
}

func deleteSecretGeneration(secrets Store, key string, generation generation) error {
	manifest := vaultManifest{Generation: generation.Generation, Chunks: generation.Chunks}
	for i := 0; i < generation.Chunks; i++ {
		if err := secrets.Delete(chunkKey(key, manifest, i)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// Windows Credential Manager limits each generic secret to 2560 bytes. Commit
// a manifest only after all chunks of the opaque sign-in exist.
func Save(secrets Store, key string, data []byte) error {
	if len(data) == 0 {
		return errors.New("empty plugin credential")
	}
	if len(data) > 256000 {
		return errors.New("plugin credential is too large to save securely")
	}
	old, oldErr := readManifest(secrets, key)
	if oldErr != nil && !errors.Is(oldErr, mygo.ErrSecretNotFound) && !errors.Is(oldErr, errInvalidManifest) {
		return oldErr
	}
	checksum := sha256.Sum256(data)
	manifest := vaultManifest{Generation: newID(), Chunks: (len(data) + 1999) / 2000, SHA256: hex.EncodeToString(checksum[:])}
	if oldErr == nil {
		for _, retired := range old.Retired {
			if err := deleteSecretGeneration(secrets, key, retired); err != nil {
				manifest.Retired = append(manifest.Retired, retired)
			}
		}
		if len(manifest.Retired) >= 16 {
			return errors.New("could not remove older plugin sign-ins from the credential store")
		}
		manifest.Retired = append(manifest.Retired, generation{Generation: old.Generation, Chunks: old.Chunks})
	}
	cleanup := func() {
		for i := 0; i < manifest.Chunks; i++ {
			_ = secrets.Delete(chunkKey(key, manifest, i))
		}
	}
	for i := 0; i < manifest.Chunks; i++ {
		if err := secrets.Set(chunkKey(key, manifest, i), data[i*2000:min(len(data), (i+1)*2000)]); err != nil {
			cleanup()
			return err
		}
	}
	encoded, _ := json.Marshal(manifest)
	if err := secrets.Set(key, encoded); err != nil {
		cleanup()
		return err
	}
	if len(manifest.Retired) > 0 {
		pending := make([]generation, 0, len(manifest.Retired))
		for _, retired := range manifest.Retired {
			if err := deleteSecretGeneration(secrets, key, retired); err != nil {
				pending = append(pending, retired)
			}
		}
		manifest.Retired = pending
		encoded, _ := json.Marshal(manifest)
		// If this cleanup commit fails, the durable manifest still names all
		// older chunks, so sign-out can retry deleting them safely.
		if err := secrets.Set(key, encoded); err != nil {
			return err
		}
		if len(pending) > 0 {
			return errors.New("renewed sign-in saved; could not remove an older plugin credential")
		}
	}
	return nil
}

func Load(secrets Store, key string) ([]byte, error) {
	manifest, err := readManifest(secrets, key)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, manifest.Chunks*2000)
	for i := 0; i < manifest.Chunks; i++ {
		chunk, err := secrets.Get(chunkKey(key, manifest, i))
		if err != nil {
			return nil, errors.New("saved plugin sign-in is incomplete; reconnect this provider")
		}
		if len(chunk) > 2000 {
			return nil, errors.New("saved plugin sign-in chunk is invalid")
		}
		data = append(data, chunk...)
	}
	checksum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(checksum[:]), manifest.SHA256) {
		return nil, errors.New("saved plugin sign-in checksum did not match")
	}
	return data, nil
}

func Delete(secrets Store, key string) error {
	manifest, err := readManifest(secrets, key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	if errors.Is(err, errInvalidManifest) {
		// Never interpret malformed data as credential key paths. Removing the
		// corrupt index permits a fresh sign-in instead of trapping the user.
		return secrets.Delete(key)
	}
	if err != nil {
		return err
	}
	for _, generation := range append(manifest.Retired, generation{Generation: manifest.Generation, Chunks: manifest.Chunks}) {
		if err := deleteSecretGeneration(secrets, key, generation); err != nil {
			return err
		}
	}
	if err := secrets.Delete(key); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
		return err
	}
	return nil
}

type Store interface {
	Get(string) ([]byte, error)
	Set(string, []byte) error
	Delete(string) error
}

func newID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func GitHubKey(id string) string { return "github-copilot-token:" + id }
