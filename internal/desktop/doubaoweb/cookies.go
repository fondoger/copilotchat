package doubaoweb

import (
	"context"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
	"github.com/fondoger/copilotchat/internal/store"
)

// RequireLogin checks the shared browser sign-in before a temporary page is made.
// Cookie export includes HttpOnly values; they never leave this transport.
func RequireLogin(ctx context.Context) error {
	_, _, err := loadCookies(ctx, store.DoubaoChatURL)
	return err
}

func loadCookies(ctx context.Context, endpoint string) (http.CookieJar, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	cookies, err := mygo.Cookies.List(mygo.CookieFilter{URL: endpoint})
	if err != nil {
		return nil, "", unavailable(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	jar, csrf, signedIn := cookieJar(cookies)
	if !signedIn {
		return nil, "", doubao.LoginRequired()
	}
	return jar, csrf, nil
}

func cookieJar(cookies []mygo.Cookie) (http.CookieJar, string, bool) {
	jar, _ := cookiejar.New(nil)
	endpoint, _ := url.Parse(store.DoubaoChatURL)
	values := make([]*http.Cookie, 0, len(cookies))
	for _, cookie := range cookies {
		domain := cookie.Domain
		if domain == store.DoubaoHost {
			domain = "" // MyGo uses a leading dot to distinguish domain cookies.
		}
		values = append(values, &http.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: cookie.Path, Secure: cookie.Secure, HttpOnly: cookie.HTTPOnly, Expires: cookie.Expires})
	}
	jar.SetCookies(endpoint, values)
	csrf, signedIn := "", false
	for _, cookie := range jar.Cookies(endpoint) {
		if (cookie.Name == "sessionid" || cookie.Name == "sessionid_ss") && cookie.Value != "" {
			signedIn = true
		}
		if cookie.Name == "passport_csrf_token" || csrf == "" && cookie.Name == "passport_csrf_token_default" {
			csrf = cookie.Value
		}
	}
	return jar, csrf, signedIn
}

func responseCookie(source *url.URL, cookie *http.Cookie, now time.Time) (mygo.Cookie, bool) {
	if source.Scheme != "https" || source.Host != store.DoubaoHost || cookie.Partitioned {
		return mygo.Cookie{}, false
	}
	domain := strings.ToLower(strings.TrimPrefix(cookie.Domain, "."))
	if domain == "" {
		domain = store.DoubaoHost
	} else {
		if domain != store.DoubaoHost && domain != "doubao.com" {
			return mygo.Cookie{}, false
		}
		domain = "." + domain
	}
	path := cookie.Path
	if !strings.HasPrefix(path, "/") {
		path = "/"
		if i := strings.LastIndexByte(source.Path, '/'); i > 0 {
			path = source.Path[:i]
		}
	}
	expires := cookie.Expires
	if cookie.MaxAge < 0 {
		expires = time.Unix(1, 0)
	} else if cookie.MaxAge > 0 {
		// Match the browser's lifetime cap and avoid overflowing time.Duration.
		expires = now.Add(time.Duration(min(cookie.MaxAge, 400*24*60*60)) * time.Second)
	}
	sameSite := mygo.CookieSameSiteLax
	switch cookie.SameSite {
	case http.SameSiteStrictMode:
		sameSite = mygo.CookieSameSiteStrict
	case http.SameSiteNoneMode:
		sameSite = mygo.CookieSameSiteNone
	}
	return mygo.Cookie{Name: cookie.Name, Value: cookie.Value, Domain: domain, Path: path, Expires: expires, Secure: cookie.Secure, HTTPOnly: cookie.HttpOnly, SameSite: sameSite}, true
}
