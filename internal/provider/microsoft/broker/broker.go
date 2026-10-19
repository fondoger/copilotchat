// Package broker uses the Copilot CLI's published SDK protocol to open its
// Microsoft Entra broker and refresh its model catalog. It never handles tokens.
package broker

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const gateway = "https://df.loki.delve.office.com/api/v1/llmapi"

// SignIn opens the CLI's native Microsoft account selector, then selects the
// Microsoft 365 account. home must be a persistent directory owned by this app,
// separate from the user's normal Copilot CLI configuration. Tokens stay in the
// operating-system vault; plaintext persistence is always declined.
func SignIn(ctx context.Context, home string) error {
	if err := isolatedHome(home); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	s, err := start(ctx, home, true)
	if err != nil {
		return err
	}
	defer s.close()
	return signIn(ctx, s)
}

// Refresh asks the selected Microsoft 365 account for its model catalog. This
// renews the CLI's broker credential without sending a prompt or doing inference.
// It does not select a different account or modify the user's account choice.
func Refresh(ctx context.Context, home string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	s, err := start(ctx, home, false)
	if err != nil {
		return err
	}
	defer s.close()
	return refresh(ctx, s)
}

// Available verifies that the installed CLI exposes the Microsoft Entra broker.
// It does not enumerate user identities, read tokens, or open a sign-in dialog.
func Available(ctx context.Context, home string) error {
	if err := isolatedHome(home); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	s, err := start(ctx, home, true)
	if err != nil {
		return err
	}
	defer s.close()
	return available(ctx, s)
}

type session struct {
	conn *connection
	id   string
}

func (s *session) call(ctx context.Context, method string, params map[string]any, result any) error {
	if params == nil {
		params = map[string]any{}
	}
	params["sessionId"] = s.id
	return s.conn.call(ctx, method, params, result)
}

func (s *session) close() {
	// The transport only terminates its dedicated authentication child process,
	// so stopping cannot affect a user's open CLI session.
	s.conn.close()
}

func start(ctx context.Context, home string, noAutoLogin bool) (*session, error) {
	if !filepath.IsAbs(home) || strings.TrimSpace(home) == "" {
		return nil, errors.New("Microsoft sign-in requires an absolute account settings directory")
	}
	path, err := exec.LookPath("copilot")
	if err != nil {
		return nil, errors.New("install or update Copilot CLI to use Microsoft account sign-in")
	}
	if err := os.MkdirAll(home, 0700); err != nil {
		return nil, errors.New("could not prepare Microsoft account settings")
	}
	args := []string{"--headless", "--stdio", "--no-auto-update", "--log-level", "error"}
	if noAutoLogin {
		args = append(args, "--no-auto-login")
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Dir = home
	cmd.Env = environment(os.Environ(), home)
	hideProcess(cmd)
	conn, err := startConnection(cmd)
	if err != nil {
		return nil, errors.New("could not start Copilot CLI for Microsoft account sign-in")
	}
	ok := false
	defer func() {
		if !ok {
			conn.close()
		}
	}()
	var handshake struct {
		ProtocolVersion int `json:"protocolVersion"`
	}
	err = conn.call(ctx, "connect", map[string]any{
		"supportedTaskKinds": []string{},
		"clientInfo":         map[string]string{"editorName": "CopilotChat"},
	}, &handshake)
	if err != nil || handshake.ProtocolVersion != 3 {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if err != nil {
			return nil, fmt.Errorf("could not connect to Copilot CLI for Microsoft sign-in: %w", err)
		}
		return nil, fmt.Errorf("update Copilot CLI: Microsoft account sign-in requires SDK protocol version 3 (installed: %d)", handshake.ProtocolVersion)
	}
	var created struct {
		SessionID string `json:"sessionId"`
	}
	err = conn.call(ctx, "session.create", map[string]any{
		"clientName":                         "CopilotChat Microsoft sign-in",
		"workingDirectory":                   home,
		"configDir":                          home,
		"availableTools":                     []string{},
		"mcpServers":                         map[string]any{},
		"customAgents":                       []any{},
		"enableConfigDiscovery":              false,
		"enableFileHooks":                    false,
		"enableHostGitOperations":            false,
		"enableSessionStore":                 false,
		"enableGitHubCacheStore":             false,
		"enableSkills":                       false,
		"enableManagedSettings":              false,
		"enableSessionTelemetry":             false,
		"enableOnDemandInstructionDiscovery": false,
		"requestPermission":                  false,
		"requestUserInput":                   false,
	}, &created)
	if err != nil {
		return nil, fmt.Errorf("could not initialize Microsoft account sign-in: %w", err)
	}
	if created.SessionID == "" {
		return nil, errors.New("Copilot CLI did not initialize Microsoft account sign-in")
	}
	ok = true
	return &session{conn: conn, id: created.SessionID}, nil
}

func isolatedHome(home string) error {
	if !filepath.IsAbs(home) {
		return errors.New("Microsoft sign-in requires its own account settings directory")
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return errors.New("could not locate Microsoft account settings")
	}
	for _, existing := range []string{filepath.Join(userHome, ".copilot"), os.Getenv("COPILOT_HOME")} {
		if existing != "" {
			absolute, err := filepath.Abs(existing)
			if err == nil && strings.EqualFold(filepath.Clean(home), filepath.Clean(absolute)) {
				return errors.New("Microsoft sign-in must use separate settings to preserve your Copilot CLI account")
			}
		}
	}
	return nil
}

func environment(env []string, home string) []string {
	result := make([]string, 0, len(env)+1)
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key == "COPILOT_HOME" || key == "COPILOT_DISABLE_KEYTAR" || key == "COPILOT_RUNTIME_PROCESS_FILE_LOGGING" ||
			key == "GH_TOKEN" || key == "GITHUB_TOKEN" ||
			strings.HasPrefix(key, "COPILOT_PROVIDER_") ||
			strings.HasPrefix(key, "COPILOT_LOKI_") ||
			strings.Contains(key, "COPILOT") && strings.Contains(key, "TOKEN") {
			continue
		}
		result = append(result, entry)
	}
	return append(result, "COPILOT_HOME="+home)
}
