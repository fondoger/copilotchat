package credentials

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/cli"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/wam"
)

type TokenSource interface {
	Token(context.Context) (string, error)
	Revoke()
}

// MicrosoftSource restores exactly the saved identity; it never opens a picker.
func MicrosoftSource(dataDir, id string, entry MicrosoftSignIn) (TokenSource, entra.Account, error) {
	switch entry.Mode {
	case "windows-wam":
		if entry.WAM != nil {
			source, err := wam.NewSource(*entry.WAM)
			return source, entry.WAM.Account, err
		}
	case "copilot-cli", "copilot-broker":
		if entry.CLI != nil {
			if entry.Mode == "copilot-cli" {
				source, err := cli.NewSource(*entry.CLI)
				return source, entry.CLI.Account, err
			}
			if dataDir == "" || id == "" {
				return nil, entra.Account{}, errors.New("could not locate the Microsoft sign-in profile")
			}
			// Saved provider IDs never become path components directly.
			key := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
			root, err := filepath.Abs(filepath.Join(dataDir, "microsoft-accounts", key))
			if err != nil {
				return nil, entra.Account{}, err
			}
			source, err := cli.NewBrokerSource(*entry.CLI, root)
			return source, entry.CLI.Account, err
		}
	}
	return nil, entra.Account{}, errors.New("invalid saved Microsoft sign-in; reconnect this provider")
}
