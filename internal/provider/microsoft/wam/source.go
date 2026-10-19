// Package wam signs in through Windows Web Account Manager. Windows owns the
// credentials; this app persists only the selected account's identity.
package wam

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/provider/microsoft/entra"
)

type Reference struct {
	ID      string        `json:"id"`
	Account entra.Account `json:"account"`
}

type acquireFunc func(context.Context, uintptr, string) (string, string, error)

var identityGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func SignIn(ctx context.Context, hwnd uintptr) (Reference, error) {
	return signIn(ctx, hwnd, acquire)
}

func signIn(ctx context.Context, hwnd uintptr, fetch acquireFunc) (Reference, error) {
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	token, id, err := fetch(ctx, hwnd, "")
	if ctx.Err() != nil {
		return Reference{}, ctx.Err()
	}
	if err != nil {
		return Reference{}, err
	}
	if strings.TrimSpace(id) == "" {
		return Reference{}, errors.New("Microsoft sign-in did not return an account identifier")
	}
	account, _, err := entra.ValidateAccessToken(token, entra.Account{})
	if err != nil {
		return Reference{}, err
	}
	return Reference{ID: id, Account: account}, nil
}

type Source struct {
	ref      Reference
	fetch    acquireFunc
	lifetime context.Context
	cancel   context.CancelFunc
	refresh  chan struct{}
	mu       sync.Mutex
	token    string
	expires  time.Time
	revoked  bool
}

func NewSource(ref Reference) (*Source, error) {
	if strings.TrimSpace(ref.ID) == "" || !identityGUID.MatchString(ref.Account.ID) || !identityGUID.MatchString(ref.Account.TenantID) {
		return nil, errors.New("invalid saved Microsoft account; reconnect in Providers")
	}
	ref.Account.ID = strings.ToLower(ref.Account.ID)
	ref.Account.TenantID = strings.ToLower(ref.Account.TenantID)
	lifetime, cancel := context.WithCancel(context.Background())
	return &Source{ref: ref, fetch: acquire, lifetime: lifetime, cancel: cancel, refresh: make(chan struct{}, 1)}, nil
}

func (s *Source) cached(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return "", errors.New("Microsoft provider has been disconnected")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if time.Until(s.expires) > 5*time.Minute {
		return s.token, nil
	}
	return "", nil
}

// Token renews only the stored WAM account and checks both its opaque ID and the
// token's tenant/object identity before returning or caching any credential.
func (s *Source) Token(ctx context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	stop := context.AfterFunc(s.lifetime, cancel)
	defer func() { stop(); cancel() }()
	if token, err := s.cached(ctx); token != "" || err != nil {
		return token, err
	}
	select {
	case s.refresh <- struct{}{}:
		defer func() { <-s.refresh }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if token, err := s.cached(ctx); token != "" || err != nil {
		return token, err
	}
	token, id, err := s.fetch(ctx, 0, s.ref.ID)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	if id != s.ref.ID {
		return "", errors.New("Microsoft sign-in returned a different account; reconnect this provider")
	}
	_, expires, err := entra.ValidateAccessToken(token, s.ref.Account)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return "", errors.New("Microsoft provider has been disconnected")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	s.token, s.expires = token, expires
	return token, nil
}

// Revoke disconnects this source without signing the user out of Windows.
// Native calls run outside the mutex, so cancellation never waits for WAM.
func (s *Source) Revoke() {
	s.mu.Lock()
	s.revoked, s.token, s.expires = true, "", time.Time{}
	s.mu.Unlock()
	s.cancel()
}
