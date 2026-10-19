// Package apikeyhelper supplies credentials to external agents without starting
// the GUI, a local server, or an interactive sign-in flow.
package apikeyhelper

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"reflect"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/credentials"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
	"github.com/fondoger/copilotchat/internal/store"
)

const Name = "copilot-chat-api-key-helper"

func Token(ctx context.Context, dir, id string, secrets credentials.Store) (string, error) {
	return token(ctx, dir, id, secrets, credentials.MicrosoftSource)
}

func token(ctx context.Context, dir, id string, secrets credentials.Store, restore func(string, string, credentials.MicrosoftSignIn) (credentials.TokenSource, entra.Account, error)) (string, error) {
	if id == "" {
		return "", errors.New("--provider is required")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	p, err := store.ReadProvider(dir, id)
	if err != nil {
		return "", err
	}
	var value string
	var unchanged func() bool
	switch p.Kind {
	case store.ProviderKindGitHub:
		if _, err := github.NormalizeHost(p.Host); err != nil {
			return "", err
		}
		saved, err := secrets.Get(credentials.GitHubKey(id))
		if err != nil {
			return "", errors.New("GitHub sign-in is unavailable; reconnect this provider in Copilot Chat")
		}
		value = string(saved)
		unchanged = func() bool {
			current, err := secrets.Get(credentials.GitHubKey(id))
			return err == nil && bytes.Equal(saved, current)
		}
	case store.ProviderKindM365:
		entry, err := credentials.LoadMicrosoft(secrets, id)
		if err != nil {
			return "", errors.New("Microsoft sign-in is unavailable; reconnect this provider in Copilot Chat")
		}
		source, _, err := restore(dir, id, entry)
		if err != nil {
			return "", err
		}
		defer source.Revoke()
		value, err = source.Token(ctx)
		if err != nil {
			return "", err
		}
		unchanged = func() bool {
			current, err := credentials.LoadMicrosoft(secrets, id)
			return err == nil && reflect.DeepEqual(entry, current)
		}
	default:
		return "", errors.New("unsupported provider kind")
	}
	// Re-read after a slow WAM/CLI renewal so a sign-out, deletion, or disable
	// completed during acquisition cannot hand a credential to the agent.
	current, err := store.ReadProvider(dir, id)
	if err != nil {
		return "", err
	}
	if current.Kind != p.Kind || current.Host != p.Host || !unchanged() {
		return "", errors.New("provider sign-in changed; retry after reconnecting")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		return "", errors.New("provider returned an empty or invalid credential")
	}
	return value, nil
}

// Run keeps stdout reserved for one credential. Check mode validates the same
// path but reports readiness on stderr, allowing diagnostics without disclosure.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, stderr, func(ctx context.Context, dir, id string) (string, error) {
		return Token(ctx, dir, id, mygo.Secrets.Service("Copilot Chat"))
	})
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer, fetch func(context.Context, string, string) (string, error)) int {
	flags := flag.NewFlagSet(Name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	id := flags.String("provider", "", "Saved Copilot Chat provider ID (required)")
	dir := flags.String("data-dir", "", "Copilot Chat settings directory")
	check := flags.Bool("check", false, "Check credentials without printing the token")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *id == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "--provider is required; no positional arguments are accepted")
		return 2
	}
	root, err := store.DataDir(*dir)
	if err != nil {
		fmt.Fprintln(stderr, "Could not locate Copilot Chat settings")
		return 1
	}
	ctx, cancel := context.WithTimeout(ctx, 70*time.Second)
	defer cancel()
	value, err := fetch(ctx, root, *id)
	if err != nil {
		fmt.Fprintln(stderr, "Copilot Chat:", err)
		return 1
	}
	if value == "" || strings.ContainsAny(value, " \t\r\n\x00") {
		fmt.Fprintln(stderr, "Copilot Chat: invalid credential")
		return 1
	}
	if *check {
		fmt.Fprintln(stderr, "Copilot Chat credential is ready.")
		return 0
	}
	if _, err := fmt.Fprintln(stdout, value); err != nil {
		return 1
	}
	return 0
}
