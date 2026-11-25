// Package wam signs in through Windows Web Account Manager. Windows owns the
// credentials; this app persists only the selected account's identity.
package wam

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

// Reference stores the selected broker account and its trusted profile binding.
// It contains no access token and must be persisted by the host, not a plugin.
type Reference struct {
	ID          string  `json:"id"`
	ProfileHash string  `json:"profileHash"`
	Account     Account `json:"account"`
}

// acquireFunc uses an empty account ID only for interactive sign-in.
type acquireFunc func(context.Context, uintptr, registration, string) (string, string, error)

func SignIn(ctx context.Context, hwnd uintptr, profile Profile) (Reference, error) {
	return signIn(ctx, hwnd, profile, acquire)
}

func signIn(ctx context.Context, hwnd uintptr, profile Profile, fetch acquireFunc) (Reference, error) {
	if err := profile.Validate(); err != nil {
		return Reference{}, err
	}
	reg, binding := profile.registration(), profile.hash()
	if err := ctx.Err(); err != nil {
		return Reference{}, err
	}
	token, id, err := fetch(ctx, hwnd, reg, "")
	if ctx.Err() != nil {
		return Reference{}, ctx.Err()
	}
	if err != nil {
		return Reference{}, err
	}
	if strings.TrimSpace(id) == "" {
		return Reference{}, errors.New("WAM sign-in did not return an account identifier")
	}
	account, _, err := validateAccessToken(token, Account{}, reg, time.Now())
	if err != nil {
		return Reference{}, err
	}
	return Reference{ID: id, ProfileHash: binding, Account: account}, nil
}

type Source struct {
	ref      Reference
	reg      registration
	fetch    acquireFunc
	lifetime context.Context
	cancel   context.CancelFunc
	refresh  chan struct{}
	mu       sync.Mutex
	token    string
	expires  time.Time
	revoked  bool
}

func NewSource(ref Reference, profile Profile) (*Source, error) {
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if ref.ProfileHash != profile.hash() {
		return nil, errors.New("the WAM profile has changed; sign in again")
	}
	if strings.TrimSpace(ref.ID) == "" || !validID(ref.Account.ID) || !validID(ref.Account.TenantID) {
		return nil, errors.New("invalid saved WAM account; sign in again")
	}
	ref.Account.ID = strings.ToLower(ref.Account.ID)
	ref.Account.TenantID = strings.ToLower(ref.Account.TenantID)
	lifetime, cancel := context.WithCancel(context.Background())
	return &Source{ref: ref, reg: profile.registration(), fetch: acquire, lifetime: lifetime, cancel: cancel, refresh: make(chan struct{}, 1)}, nil
}

func (s *Source) cached(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return "", errors.New("WAM account has been disconnected")
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
	token, id, err := s.fetch(ctx, 0, s.reg, s.ref.ID)
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		return "", err
	}
	if id != s.ref.ID {
		return "", errors.New("WAM sign-in returned a different account; reconnect this provider")
	}
	_, expires, err := validateAccessToken(token, s.ref.Account, s.reg, time.Now())
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return "", errors.New("WAM account has been disconnected")
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
