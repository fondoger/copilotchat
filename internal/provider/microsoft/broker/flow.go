package broker

import (
	"context"
	"errors"
	"strings"
	"time"
)

type caller interface {
	call(context.Context, string, map[string]any, any) error
}

type account struct {
	Kind        string `json:"kind"`
	Host        string `json:"host"`
	Login       string `json:"login"`
	SelectionID string `json:"selectionId"`
}

type loginStep struct {
	Kind   string `json:"kind"`
	Result struct {
		Status   string    `json:"status"`
		Host     string    `json:"host"`
		Login    string    `json:"login"`
		Accounts []account `json:"accounts"`
	} `json:"result"`
	Message string `json:"message"`
}

func available(ctx context.Context, s caller) error {
	var providers struct {
		Kind  string `json:"kind"`
		Items []struct {
			Kind      string `json:"kind"`
			Available bool   `json:"available"`
		} `json:"items"`
	}
	if err := s.call(ctx, "session.accounts.enumerate", map[string]any{
		"query": map[string]string{"kind": "providers"},
	}, &providers); err != nil {
		return err
	}
	for _, provider := range providers.Items {
		if providers.Kind == "providers" && provider.Kind == "entra" && provider.Available {
			return nil
		}
	}
	return errors.New("Microsoft account sign-in is unavailable; update Copilot CLI to a version supporting Microsoft Entra on this computer")
}

func signIn(ctx context.Context, s caller) error {
	if err := available(ctx, s); err != nil {
		return err
	}
	var begun struct {
		FlowID string    `json:"flowId"`
		Step   loginStep `json:"step"`
	}
	if err := s.call(ctx, "session.accounts.login.begin", map[string]any{
		"kind": "entra", "mode": "selectAccount",
	}, &begun); err != nil {
		return err
	}
	if begun.FlowID == "" {
		return errors.New("Copilot CLI did not start Microsoft account sign-in")
	}
	completed := false
	defer func() {
		if !completed {
			cancelCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = s.call(cancelCtx, "session.accounts.login.cancel", map[string]any{"flowId": begun.FlowID}, nil)
		}
	}()
	step := begun.Step
	for count := 0; count < 2000; count++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		params := map[string]any{"flowId": begun.FlowID}
		switch step.Kind {
		case "needs-interaction":
			// Entra owns the system broker/browser UI. No URL or device code is
			// supplied by the host; advancing lets the CLI invoke that broker.
		case "awaiting":
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		case "completed":
			switch step.Result.Status {
			case "completed":
				if step.Result.Host != gateway || strings.TrimSpace(step.Result.Login) == "" {
					return errors.New("this account does not provide Microsoft 365 Copilot access for this service")
				}
				completed = true
				return nil
			case "needs-account-selection":
				var selected string
				for _, candidate := range step.Result.Accounts {
					if candidate.Kind == "loki" && candidate.Host == gateway && candidate.SelectionID != "" {
						if selected != "" {
							return errors.New("Microsoft sign-in returned multiple work accounts; choose one account and try again")
						}
						selected = candidate.SelectionID
					}
				}
				if selected == "" {
					return errors.New("the signed-in account is not entitled to Microsoft 365 Copilot for this service")
				}
				params["input"] = selected
			case "needs-plaintext-consent":
				return errors.New("the operating-system credential vault is unavailable; Microsoft sign-in cannot store credentials securely")
			case "declined":
				return context.Canceled
			default:
				return errors.New("Copilot CLI returned an unsupported Microsoft sign-in result; update Copilot CLI")
			}
		case "error":
			return errors.New("Microsoft account sign-in failed: " + safeMessage(step.Message))
		default:
			return errors.New("Copilot CLI did not offer native Microsoft account sign-in; update Copilot CLI")
		}
		var next loginStep
		if err := s.call(ctx, "session.accounts.login.advance", params, &next); err != nil {
			return err
		}
		step = next
	}
	return errors.New("Microsoft account sign-in did not finish; try again")
}

func refresh(ctx context.Context, s caller) error {
	var active struct {
		Kind    string   `json:"kind"`
		Account *account `json:"account"`
	}
	if err := s.call(ctx, "session.accounts.get", map[string]any{
		"query": map[string]string{"kind": "activeAccount"},
	}, &active); err != nil {
		return err
	}
	if active.Kind != "activeAccount" || active.Account == nil || active.Account.Kind != "loki" || active.Account.Host != gateway {
		return errors.New("sign in with your Microsoft work account again in Providers")
	}
	return s.call(ctx, "session.model.list", map[string]any{"skipCache": true}, nil)
}
