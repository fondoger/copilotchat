package doubaoweb

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/egoist/mygo"
)

func TestCookieSnapshotKeepsLoginAndScope(t *testing.T) {
	now := time.Now()
	jar, csrf, signedIn := cookieJar([]mygo.Cookie{
		{Name: "sessionid", Value: "test-session", Domain: ".doubao.com", Path: "/", Secure: true, HTTPOnly: true},
		{Name: "passport_csrf_token_default", Value: "fallback", Domain: ".doubao.com", Path: "/"},
		{Name: "passport_csrf_token", Value: "current", Domain: "www.doubao.com", Path: "/"},
		{Name: "expired", Value: "old", Domain: ".doubao.com", Path: "/", Expires: now.Add(-time.Hour)},
	})
	if !signedIn || csrf != "current" {
		t.Fatal("login or current CSRF token missing")
	}
	u, _ := url.Parse("https://other.www.doubao.com/")
	for _, cookie := range jar.Cookies(u) {
		if cookie.Name == "passport_csrf_token" || cookie.Name == "expired" {
			t.Fatalf("host-only or expired cookie leaked to another host: %s", cookie.Name)
		}
	}
	_, _, signedIn = cookieJar([]mygo.Cookie{{Name: "sessionid", Value: "old", Domain: ".doubao.com", Expires: now.Add(-time.Hour)}})
	if signedIn {
		t.Fatal("expired session accepted as signed in")
	}
}

func TestResponseCookiesPreserveRotationAndDeletion(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	u, _ := url.Parse("https://www.doubao.com/chat/completion")
	for _, domain := range []string{"doubao.com", ".doubao.com"} {
		cookie, ok := responseCookie(u, &http.Cookie{Name: "sessionid", Value: "new", Domain: domain, MaxAge: 60, Secure: true, SameSite: http.SameSiteNoneMode}, now)
		if !ok || cookie.Domain != ".doubao.com" || cookie.Path != "/chat" || !cookie.Expires.Equal(now.Add(time.Minute)) || cookie.SameSite != mygo.CookieSameSiteNone {
			t.Fatalf("rotation changed cookie scope or lifetime: %+v", cookie)
		}
	}
	cookie, ok := responseCookie(u, &http.Cookie{Name: "sessionid", Domain: ".doubao.com", Path: "/", MaxAge: -1}, now)
	if !ok || cookie.Domain != ".doubao.com" || cookie.Expires.IsZero() || !cookie.Expires.Before(now) {
		t.Fatal("Max-Age=0 did not delete the original domain cookie")
	}
	cookie, ok = responseCookie(u, &http.Cookie{Name: "host-only", Path: "/"}, now)
	if !ok || cookie.Domain != "www.doubao.com" {
		t.Fatal("host-only cookie changed scope")
	}
	for _, input := range []*http.Cookie{{Domain: "attacker.invalid"}, {Partitioned: true}} {
		if _, ok := responseCookie(u, input, now); ok {
			t.Fatal("unsupported cookie accepted into the shared browser store")
		}
	}
}
