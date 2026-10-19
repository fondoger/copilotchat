package desktop

import (
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

type m365SavedSignIn struct {
	Version int            `json:"version"`
	Mode    string         `json:"mode"`
	CLI     *cli.Reference `json:"cli,omitempty"`
	WAM     *wam.Reference `json:"wam,omitempty"`
}

type secretManifest struct {
	Generation string             `json:"generation"`
	Chunks     int                `json:"chunks"`
	SHA256     string             `json:"sha256"`
	Retired    []secretGeneration `json:"retired,omitempty"`
}

type secretGeneration struct {
	Generation string `json:"generation"`
	Chunks     int    `json:"chunks"`
}

var errInvalidMicrosoftManifest = errors.New("invalid saved Microsoft sign-in; reconnect this provider")

func validSecretGeneration(generation string, chunks int) bool {
	decoded, err := hex.DecodeString(generation)
	return err == nil && len(decoded) == 12 && chunks >= 1 && chunks <= 128
}

func m365CredentialKey(id string) string { return "m365-copilot:" + id }
func m365ChunkKey(key string, manifest secretManifest, index int) string {
	return fmt.Sprintf("%s:%s:%d", key, manifest.Generation, index)
}

func readSecretManifest(secrets credentialStore, key string) (secretManifest, error) {
	data, err := secrets.Get(key)
	if err != nil {
		return secretManifest{}, err
	}
	var manifest secretManifest
	if json.Unmarshal(data, &manifest) != nil || !validSecretGeneration(manifest.Generation, manifest.Chunks) || len(manifest.Retired) > 16 {
		return secretManifest{}, errInvalidMicrosoftManifest
	}
	if checksum, err := hex.DecodeString(manifest.SHA256); err != nil || len(checksum) != 32 {
		return secretManifest{}, errInvalidMicrosoftManifest
	}
	seen := map[string]bool{manifest.Generation: true}
	for _, retired := range manifest.Retired {
		if !validSecretGeneration(retired.Generation, retired.Chunks) || seen[retired.Generation] {
			return secretManifest{}, errInvalidMicrosoftManifest
		}
		seen[retired.Generation] = true
	}
	return manifest, nil
}

func deleteSecretGeneration(secrets credentialStore, key string, generation secretGeneration) error {
	manifest := secretManifest{Generation: generation.Generation, Chunks: generation.Chunks}
	for i := 0; i < generation.Chunks; i++ {
		if err := secrets.Delete(m365ChunkKey(key, manifest, i)); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
			return err
		}
	}
	return nil
}

// Windows Credential Manager limits each generic secret to 2560 bytes. Commit
// a manifest only after all account-reference chunks exist. Access tokens remain
// managed by Windows WAM or Copilot CLI; only account references are saved here.
func saveM365SignIn(secrets credentialStore, id string, entry m365SavedSignIn) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	if len(data) > 256000 {
		return errors.New("Microsoft credential is too large to save securely")
	}
	key := m365CredentialKey(id)
	old, oldErr := readSecretManifest(secrets, key)
	if oldErr != nil && !errors.Is(oldErr, mygo.ErrSecretNotFound) && !errors.Is(oldErr, errInvalidMicrosoftManifest) {
		return oldErr
	}
	checksum := sha256.Sum256(data)
	manifest := secretManifest{Generation: newID(), Chunks: (len(data) + 1999) / 2000, SHA256: hex.EncodeToString(checksum[:])}
	if oldErr == nil {
		for _, retired := range old.Retired {
			if err := deleteSecretGeneration(secrets, key, retired); err != nil {
				manifest.Retired = append(manifest.Retired, retired)
			}
		}
		if len(manifest.Retired) >= 16 {
			return errors.New("could not remove older Microsoft sign-ins from the credential store")
		}
		manifest.Retired = append(manifest.Retired, secretGeneration{Generation: old.Generation, Chunks: old.Chunks})
	}
	cleanup := func() {
		for i := 0; i < manifest.Chunks; i++ {
			_ = secrets.Delete(m365ChunkKey(key, manifest, i))
		}
	}
	for i := 0; i < manifest.Chunks; i++ {
		if err := secrets.Set(m365ChunkKey(key, manifest, i), data[i*2000:min(len(data), (i+1)*2000)]); err != nil {
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
		pending := make([]secretGeneration, 0, len(manifest.Retired))
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

func loadM365SignIn(secrets credentialStore, id string) (m365SavedSignIn, error) {
	key := m365CredentialKey(id)
	manifest, err := readSecretManifest(secrets, key)
	if err != nil {
		return m365SavedSignIn{}, err
	}
	data := make([]byte, 0, manifest.Chunks*2000)
	for i := 0; i < manifest.Chunks; i++ {
		chunk, err := secrets.Get(m365ChunkKey(key, manifest, i))
		if err != nil {
			return m365SavedSignIn{}, errors.New("saved Microsoft sign-in is incomplete; reconnect this provider")
		}
		if len(chunk) > 2000 {
			return m365SavedSignIn{}, errors.New("saved Microsoft sign-in chunk is invalid")
		}
		data = append(data, chunk...)
	}
	checksum := sha256.Sum256(data)
	if !strings.EqualFold(hex.EncodeToString(checksum[:]), manifest.SHA256) {
		return m365SavedSignIn{}, errors.New("saved Microsoft sign-in checksum did not match")
	}
	var entry m365SavedSignIn
	if json.Unmarshal(data, &entry) != nil || entry.Version != 1 {
		return entry, errors.New("unsupported saved Microsoft sign-in")
	}
	return entry, nil
}

func (a *app) clearM365Credentials(id string) error {
	if r := a.providers[id]; r != nil && r.m365Auth != nil {
		r.m365Auth.Revoke()
		r.m365Auth = nil
	}
	secrets, key := a.credentials(), m365CredentialKey(id)
	manifest, err := readSecretManifest(secrets, key)
	if errors.Is(err, mygo.ErrSecretNotFound) {
		return nil
	}
	if errors.Is(err, errInvalidMicrosoftManifest) {
		// Never interpret malformed data as credential key paths. Removing the
		// corrupt index permits a fresh sign-in instead of trapping the user.
		return secrets.Delete(key)
	}
	if err != nil {
		return err
	}
	for _, generation := range append(manifest.Retired, secretGeneration{Generation: manifest.Generation, Chunks: manifest.Chunks}) {
		if err := deleteSecretGeneration(secrets, key, generation); err != nil {
			return err
		}
	}
	if err := secrets.Delete(key); err != nil && !errors.Is(err, mygo.ErrSecretNotFound) {
		return err
	}
	return nil
}
