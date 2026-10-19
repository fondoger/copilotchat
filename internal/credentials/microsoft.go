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
	"github.com/fondoger/copilotchat/internal/provider/microsoft/cli"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/wam"
)

type MicrosoftSignIn struct {
	Version int            `json:"version"`
	Mode    string         `json:"mode"`
	CLI     *cli.Reference `json:"cli,omitempty"`
	WAM     *wam.Reference `json:"wam,omitempty"`
}

type Manifest struct {
	Generation string       `json:"generation"`
	Chunks     int          `json:"chunks"`
	SHA256     string       `json:"sha256"`
	Retired    []Generation `json:"retired,omitempty"`
}

type Generation struct {
	Generation string `json:"generation"`
	Chunks     int    `json:"chunks"`
}

var ErrInvalidMicrosoftManifest = errors.New("invalid saved Microsoft sign-in; reconnect this provider")

func validSecretGeneration(generation string, chunks int) bool {
	decoded, err := hex.DecodeString(generation)
	return err == nil && len(decoded) == 12 && chunks >= 1 && chunks <= 128
}

func MicrosoftKey(id string) string { return "m365-copilot:" + id }
func ChunkKey(key string, manifest Manifest, index int) string {
	return fmt.Sprintf("%s:%s:%d", key, manifest.Generation, index)
}

func ReadManifest(secrets Store, key string) (Manifest, error) {
	data, err := secrets.Get(key)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if json.Unmarshal(data, &manifest) != nil || !validSecretGeneration(manifest.Generation, manifest.Chunks) || len(manifest.Retired) > 16 {
		return Manifest{}, ErrInvalidMicrosoftManifest
	}
	if checksum, err := hex.DecodeString(manifest.SHA256); err != nil || len(checksum) != 32 {
		return Manifest{}, ErrInvalidMicrosoftManifest
	}
	seen := map[string]bool{manifest.Generation: true}
	for _, retired := range manifest.Retired {
		if !validSecretGeneration(retired.Generation, retired.Chunks) || seen[retired.Generation] {
			return Manifest{}, ErrInvalidMicrosoftManifest
		}
		seen[retired.Generation] = true
	}
	return manifest, nil
}

func deleteSecretGeneration(secrets Store, key string, generation Generation) error {
	manifest := Manifest{Generation: generation.Generation, Chunks: generation.Chunks}
	for i := 0; i < generation.Chunks; i++ {
		if err := secrets.Delete(ChunkKey(key, manifest, i)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// Windows Credential Manager limits each generic secret to 2560 bytes. Commit
// a manifest only after all account-reference chunks exist. Access tokens remain
// managed by Windows WAM or Copilot CLI; only account references are saved here.
func SaveMicrosoft(secrets Store, id string, entry MicrosoftSignIn) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(data) > 256000 {
		return errors.New("Microsoft credential is too large to save securely")
	}
	key := MicrosoftKey(id)
	old, oldErr := ReadManifest(secrets, key)
	if oldErr != nil && !errors.Is(oldErr, mygo.ErrSecretNotFound) && !errors.Is(oldErr, ErrInvalidMicrosoftManifest) {
		return oldErr
	}
	checksum := sha256.Sum256(data)
	manifest := Manifest{Generation: newID(), Chunks: (len(data) + 1999) / 2000, SHA256: hex.EncodeToString(checksum[:])}
	if oldErr == nil {
		for _, retired := range old.Retired {
			if err := deleteSecretGeneration(secrets, key, retired); err != nil {
				manifest.Retired = append(manifest.Retired, retired)
			}
		}
		if len(manifest.Retired) >= 16 {
			return errors.New("could not remove older Microsoft sign-ins from the credential store")
		}
		manifest.Retired = append(manifest.Retired, Generation{Generation: old.Generation, Chunks: old.Chunks})
	}
	cleanup := func() {
		for i := 0; i < manifest.Chunks; i++ {
			_ = secrets.Delete(ChunkKey(key, manifest, i))
		}
	}
	for i := 0; i < manifest.Chunks; i++ {
		if err := secrets.Set(ChunkKey(key, manifest, i), data[i*2000:min(len(data), (i+1)*2000)]); err != nil {
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
		pending := make([]Generation, 0, len(manifest.Retired))
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
			return errors.New("renewed sign-in saved; could not remove an older Microsoft credential")
		}
	}
	return nil
}

func LoadMicrosoft(secrets Store, id string) (MicrosoftSignIn, error) {
	key := MicrosoftKey(id)
	manifest, err := ReadManifest(secrets, key)
	if err != nil {
		return MicrosoftSignIn{}, err
	}
	data := make([]byte, 0, manifest.Chunks*2000)
	for i := 0; i < manifest.Chunks; i++ {
		chunk, err := secrets.Get(ChunkKey(key, manifest, i))
		if err != nil {
			return MicrosoftSignIn{}, errors.New("saved Microsoft sign-in is incomplete; reconnect this provider")
		}
		if len(chunk) > 2000 {
			return MicrosoftSignIn{}, errors.New("saved Microsoft sign-in chunk is invalid")
		}
		data = append(data, chunk...)
	}
	checksum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(checksum[:]), manifest.SHA256) {
		return MicrosoftSignIn{}, errors.New("saved Microsoft sign-in checksum did not match")
	}
	var entry MicrosoftSignIn
	if json.Unmarshal(data, &entry) != nil || entry.Version != 1 {
		return entry, errors.New("unsupported saved Microsoft sign-in")
	}
	return entry, nil
}

func DeleteMicrosoft(secrets Store, id string) error {
	key := MicrosoftKey(id)
	manifest, err := ReadManifest(secrets, key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	if errors.Is(err, ErrInvalidMicrosoftManifest) {
		// Never interpret malformed data as credential key paths. Removing the
		// corrupt index permits a fresh sign-in instead of trapping the user.
		return secrets.Delete(key)
	}
	if err != nil {
		return err
	}
	for _, generation := range append(manifest.Retired, Generation{Generation: manifest.Generation, Chunks: manifest.Chunks}) {
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
