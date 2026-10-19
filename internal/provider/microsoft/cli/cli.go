// Package cli reuses a selected Copilot CLI work account without copying or
// modifying the CLI's credentials. Copilot CLI remains responsible for renewal.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/broker"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
)

const gateway = "https://df.loki.delve.office.com/api/v1/llmapi"
const probeModel = "__copilotchat_credential_catalog_probe__"

// Current Copilot CLI versions store the selected work-account identity as
// entra:<object GUID>.<tenant GUID>. Read that tenant instead of restricting
// sign-in to one organization, and fail clearly if the CLI changes this format.
const guidFormat = `[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`

var identityPattern = regexp.MustCompile(`^entra:(` + guidFormat + `)\.(` + guidFormat + `)$`)
var lineComment = regexp.MustCompile(`(?m)^\s*//[^\r\n]*`)

type Reference struct {
	Account entra.Account `json:"account"`
}

type cliUser struct {
	Login       string `json:"login"`
	Host        string `json:"host"`
	Kind        string `json:"kind"`
	DerivedFrom string `json:"derivedFrom"`
}

type cliConfig struct {
	Selected cliUser   `json:"lastLoggedInUser"`
	Users    []cliUser `json:"loggedInUsers"`
}

func configDir() (string, error) {
	if root := os.Getenv("COPILOT_HOME"); root != "" {
		return filepath.Abs(root)
	}
	root, err := os.UserHomeDir()
	return filepath.Join(root, ".copilot"), err
}

func readConfig(root string) (cliConfig, error) {
	data, err := os.ReadFile(filepath.Join(root, "config.json"))
	if err != nil {
		return cliConfig{}, errors.New("open Copilot CLI and sign in with your Microsoft work account first")
	}
	var config cliConfig
	data = []byte(strings.TrimPrefix(string(data), "\ufeff"))
	if len(data) > 4<<20 || json.Unmarshal(lineComment.ReplaceAll(data, nil), &config) != nil {
		return cliConfig{}, errors.New("could not read Copilot CLI account settings")
	}
	return config, nil
}

func selectedReference(config cliConfig) (Reference, error) {
	login := strings.ToLower(strings.TrimSpace(config.Selected.Login))
	if login == "" || config.Selected.Host != gateway {
		return Reference{}, errors.New("select your Microsoft 365 work account in Copilot CLI first")
	}
	identities := map[string]entra.Account{}
	for _, user := range config.Users {
		if strings.EqualFold(user.Login, login) && user.Host == gateway && user.Kind == "loki" {
			parts := identityPattern.FindStringSubmatch(user.DerivedFrom)
			if parts == nil {
				return Reference{}, errors.New("Copilot CLI uses an unsupported Microsoft work-account identity format; reconnect after updating Copilot Chat")
			}
			account := entra.Account{ID: strings.ToLower(parts[1]), TenantID: strings.ToLower(parts[2]), Username: login}
			identities[account.ID+"."+account.TenantID] = account
		}
	}
	if len(identities) == 1 {
		for _, account := range identities {
			return Reference{Account: account}, nil
		}
	}
	return Reference{}, errors.New("Copilot CLI does not have an unambiguous Microsoft 365 account for this service")
}

// Selected returns identity only. It never returns tokens from config.json.
func Selected() (Reference, error) {
	root, err := configDir()
	if err != nil {
		return Reference{}, err
	}
	return SelectedAt(root)
}

// SelectedAt reads identity metadata from an app-owned broker profile.
func SelectedAt(root string) (Reference, error) {
	config, err := readConfig(root)
	if err != nil {
		return Reference{}, err
	}
	return selectedReference(config)
}

type Source struct {
	mu          sync.Mutex
	ref         Reference
	root        string
	lastRefresh time.Time
	read        func(context.Context, string) (string, error)
	refresh     func(context.Context, string) error
	lifetime    context.Context
	cancel      context.CancelFunc
	revoked     bool
}

func NewSource(ref Reference) (*Source, error) {
	root, err := configDir()
	if err != nil {
		return nil, err
	}
	return newSource(ref, root, refreshCatalog)
}

// NewBrokerSource uses a separate CLI profile for this provider. Renewal uses
// the SDK's model catalog RPC and never submits an inference prompt.
func NewBrokerSource(ref Reference, root string) (*Source, error) {
	if !filepath.IsAbs(root) {
		return nil, errors.New("Microsoft broker profile must have an absolute path")
	}
	return newSource(ref, root, broker.Refresh)
}

func newSource(ref Reference, root string, refresh func(context.Context, string) error) (*Source, error) {
	identity := "entra:" + ref.Account.ID + "." + ref.Account.TenantID
	if !identityPattern.MatchString(identity) || strings.TrimSpace(ref.Account.Username) == "" {
		return nil, errors.New("invalid saved Copilot CLI work account; reconnect in Providers")
	}
	ref.Account.ID = strings.ToLower(ref.Account.ID)
	ref.Account.TenantID = strings.ToLower(ref.Account.TenantID)
	ref.Account.Username = strings.ToLower(strings.TrimSpace(ref.Account.Username))
	lifetime, cancel := context.WithCancel(context.Background())
	return &Source{ref: ref, root: root, read: readCredential, refresh: refresh, lifetime: lifetime, cancel: cancel}, nil
}

func (s *Source) contextErr(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.lifetime != nil {
		return s.lifetime.Err()
	}
	return nil
}

func (s *Source) saved(ctx context.Context) (string, error) {
	identity := "entra:" + s.ref.Account.ID + "." + s.ref.Account.TenantID
	for _, suffix := range []string{":loki", ""} {
		if err := s.contextErr(ctx); err != nil {
			return "", err
		}
		token, err := s.read(ctx, "loki:"+identity+suffix)
		if canceled := s.contextErr(ctx); canceled != nil {
			return "", canceled
		}
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}
			continue
		}
		account, expires, err := entra.ValidateAccessToken(token, s.ref.Account)
		if err == nil && strings.EqualFold(account.Username, s.ref.Account.Username) && time.Until(expires) > 3*time.Minute {
			if err := s.contextErr(ctx); err != nil {
				return "", err
			}
			return token, nil
		}
	}
	return "", errors.New("the saved Copilot CLI sign-in needs renewal")
}

// Token pins tenant ID, object ID, and username. Switching the CLI's selected account
// never silently changes the account used by an existing chat provider.
func (s *Source) Token(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return "", errors.New("Microsoft provider has been disconnected")
	}
	if s.lifetime != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(ctx)
		stop := context.AfterFunc(s.lifetime, cancel)
		defer func() { stop(); cancel() }()
	}
	if err := s.contextErr(ctx); err != nil {
		return "", err
	}
	if token, err := s.saved(ctx); err == nil {
		return token, nil
	} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "", err
	}
	config, err := readConfig(s.root)
	if err != nil {
		return "", err
	}
	selected, err := selectedReference(config)
	if err != nil || !strings.EqualFold(selected.Account.ID, s.ref.Account.ID) || !strings.EqualFold(selected.Account.TenantID, s.ref.Account.TenantID) || !strings.EqualFold(selected.Account.Username, s.ref.Account.Username) {
		return "", errors.New("to renew this provider, select its Microsoft work account in Copilot CLI, or sign in directly in Providers")
	}
	if err := s.contextErr(ctx); err != nil {
		return "", err
	}
	if time.Since(s.lastRefresh) < time.Minute {
		return "", errors.New("Copilot CLI could not renew this sign-in; sign in again with Microsoft Entra in Copilot CLI")
	}
	previousAttempt := s.lastRefresh
	s.lastRefresh = time.Now()
	if err := s.refresh(ctx, s.root); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.lastRefresh = previousAttempt
		}
		return "", err
	}
	if err := s.contextErr(ctx); err != nil {
		return "", err
	}
	return s.saved(ctx)
}

// Revoke stops this app's access and pending renewal; it does not sign the user
// out of Windows or delete credentials owned by Copilot CLI.
func (s *Source) Revoke() {
	if s.cancel != nil {
		s.cancel()
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revoked = true
}

func refreshCatalog(ctx context.Context, root string) error {
	path, err := exec.LookPath("copilot")
	if err != nil {
		return errors.New("Copilot CLI is required to renew this sign-in; install it or sign in directly in Providers")
	}
	ctx, cancel := context.WithTimeout(ctx, 55*time.Second)
	defer cancel()
	dir, err := os.MkdirTemp("", "copilotchat-catalog-")
	if err != nil {
		return errors.New("could not prepare Copilot CLI account renewal")
	}
	defer os.RemoveAll(dir)
	cmd := exec.CommandContext(ctx, path, "-p", "Validate the configured model catalog only.", "--model", probeModel, "--no-custom-instructions", "--disable-builtin-mcps", "--available-tools=", "--no-ask-user", "--no-remote", "--silent", "--log-level", "error")
	cmd.Dir = dir
	cmd.Env = cleanEnvironment(os.Environ(), root)
	hideProcess(cmd)
	// Discard CLI diagnostics after checking the expected model-validation exit;
	// these may contain account metadata and must never enter UI errors or logs.
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(string(output), probeModel) {
		return errors.New("Copilot CLI could not renew the account; open Copilot CLI and sign in with Microsoft Entra again")
	}
	return nil
}

func cleanEnvironment(env []string, root string) []string {
	blocked := map[string]bool{"COPILOT_HOME": true, "COPILOT_GITHUB_TOKEN": true, "GITHUB_COPILOT_GITHUB_TOKEN": true, "GH_TOKEN": true, "GITHUB_TOKEN": true, "COPILOT_PROVIDER_API_KEY": true, "COPILOT_PROVIDER_BASE_URL": true, "COPILOT_LOKI_TOKEN": true, "COPILOT_LOKI_TOKEN_COMMAND": true}
	result := make([]string, 0, len(env)+1)
	for _, item := range env {
		key, _, _ := strings.Cut(item, "=")
		if !blocked[strings.ToUpper(key)] {
			result = append(result, item)
		}
	}
	return append(result, "COPILOT_HOME="+root)
}
