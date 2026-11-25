package plugin

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/appinfo"
	"github.com/fondoger/copilotchat/internal/provider"
	"golang.org/x/net/http/httpguts"
)

type requestSpec struct {
	URL            string            `json:"url"`
	Headers        map[string]string `json:"headers"`
	Redirects      []string          `json:"redirects"`
	ForwardHeaders http.Header       `json:"forwardHeaders"`
}
type requestParams struct {
	Version        string      `json:"version"`
	CallID         string      `json:"callId"`
	CorrelationID  string      `json:"correlationId"`
	CatalogURL     string      `json:"catalogURL,omitempty"`
	Endpoint       string      `json:"endpoint,omitempty"`
	ConversationID string      `json:"conversationId,omitempty"`
	Agent          bool        `json:"agent"`
	RequestHeaders http.Header `json:"requestHeaders,omitempty"`
}

func reservedHeader(key string) bool {
	switch key {
	case "Authorization", "Proxy-Authorization", "Proxy-Authenticate", "Cookie", "Cookie2", "Set-Cookie",
		"X-Api-Key", "Api-Key", "X-Auth-Token", "X-Access-Token", "X-Goog-Api-Key",
		"Host", "Content-Length", "Content-Encoding", "Connection", "Proxy-Connection",
		"Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade", "Expect":
		return true
	}
	return false
}

func connectionHeaders(headers http.Header) map[string]bool {
	hop := make(map[string]bool)
	for key, values := range headers {
		if !strings.EqualFold(key, "Connection") {
			continue
		}
		for _, value := range values {
			for token := range strings.SplitSeq(value, ",") {
				hop[http.CanonicalHeaderKey(strings.TrimSpace(token))] = true
			}
		}
	}
	return hop
}

func pluginRequestHeaders(headers http.Header, hop map[string]bool) http.Header {
	safe := make(http.Header)
	for name, values := range headers {
		key := http.CanonicalHeaderKey(name)
		if httpguts.ValidHeaderFieldName(name) && !reservedHeader(key) && !hop[key] {
			safe[key] = append(safe[key], values...)
		}
	}
	return safe
}

func applyForwardHeaders(dst, changes http.Header, hop map[string]bool) error {
	seen := make(map[string]bool, len(changes))
	for name, values := range changes {
		key := http.CanonicalHeaderKey(name)
		if !httpguts.ValidHeaderFieldName(name) || reservedHeader(key) || hop[key] || seen[key] {
			return fmt.Errorf("plugin forward header %q is invalid, reserved, or duplicated", name)
		}
		seen[key] = true
		if len(values) == 0 {
			dst.Del(key)
			continue
		}
		for _, value := range values {
			if !httpguts.ValidHeaderFieldValue(value) {
				return fmt.Errorf("plugin forward header %q has an invalid value", name)
			}
		}
		dst[key] = values
	}
	return nil
}

func newParams() requestParams {
	return requestParams{Version: appinfo.Version(), CallID: provider.RequestID(), CorrelationID: provider.RequestID()}
}
func (c *Client) allowed(u *url.URL) bool {
	return u != nil && u.Scheme == "https" && u.User == nil && u.Opaque == "" && u.Fragment == "" && slices.Contains(c.origins, "https://"+u.Host)
}

func (c *Client) request(ctx context.Context, method string, spec requestSpec, body []byte) (*http.Request, error) {
	r, err := http.NewRequestWithContext(ctx, method, spec.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if !c.allowed(r.URL) {
		return nil, errors.New("plugin requested an undeclared HTTPS origin")
	}
	for key, value := range spec.Headers {
		k := strings.ToLower(key)
		if reservedHeader(http.CanonicalHeaderKey(key)) ||
			(!strings.HasPrefix(k, "x-") && k != "anthropic-version" && k != "openai-beta" && k != "anthropic-beta") {
			return nil, fmt.Errorf("plugin header %q is not permitted", key)
		}
		r.Header.Set(key, value)
	}
	r.Header.Set("User-Agent", appinfo.UserAgent())
	r.Header.Set("Accept", "application/json")
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("Accept", "text/event-stream")
	}
	return r, nil
}

type responseBody struct {
	io.ReadCloser
	done func()
}

func (b *responseBody) Close() error { defer b.done(); return b.ReadCloser.Close() }

// Attach the bearer only to the private outbound request. No JS hook, gateway
// diagnostic transport or redirect handler receives the credential.
func (c *Client) Do(r *http.Request) (*http.Response, error) {
	if !c.allowed(r.URL) {
		return nil, errors.New("plugin request denied for this origin")
	}
	if err := c.lifetime.Err(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(c.lifetime, cancel)
	done := func() { stop(); cancel() }
	req := r.Clone(ctx)
	if c.source != nil {
		token, err := c.source.Token(ctx)
		if err != nil {
			done()
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		done()
		return nil, err
	}
	resp.Request = r
	resp.Body = &responseBody{resp.Body, done}
	return resp, nil
}

func (c *Client) Models(ctx context.Context) ([]provider.Model, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	var spec requestSpec
	if err := c.plugin.call(ctx, "catalog", newParams(), &spec); err != nil {
		return nil, err
	}
	for hops := 0; ; hops++ {
		req, err := c.request(ctx, http.MethodGet, spec, nil)
		if err != nil {
			return nil, err
		}
		resp, err := c.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			loc, err := resp.Location()
			resp.Body.Close()
			if err != nil || hops >= 4 || !c.allowed(loc) || !slices.Contains(spec.Redirects, loc.String()) {
				return nil, errors.New("unsupported plugin catalog redirect")
			}
			spec.URL = loc.String()
			continue
		}
		var data json.RawMessage
		if err = provider.ReadJSON(resp, &data); err != nil {
			return nil, err
		}
		var rows []struct {
			provider.Model
			Picker *bool `json:"picker"`
		}
		if err = c.plugin.call(ctx, "models", data, &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("plugin models() must return an array")
		}
		cache := make(map[string]provider.Model, len(rows))
		picker := make([]provider.Model, 0, len(rows))
		for _, row := range rows {
			if row.ID == "" || !provider.InferencePath(row.Endpoint) {
				return nil, errors.New("plugin advertised an invalid model or protocol")
			}
			if _, exists := cache[row.ID]; exists {
				return nil, fmt.Errorf("duplicate plugin model %q", row.ID)
			}
			cache[row.ID] = row.Model
			if row.Picker == nil || *row.Picker {
				picker = append(picker, row.Model)
			}
		}
		c.mu.Lock()
		c.models, c.catalogURL = cache, spec.URL
		c.mu.Unlock()
		return picker, nil
	}
}
func (c *Client) LookupModel(ctx context.Context, id string) (provider.Model, error) {
	get := func() (provider.Model, bool) { c.mu.RLock(); defer c.mu.RUnlock(); m, ok := c.models[id]; return m, ok }
	if m, ok := get(); ok {
		return m, nil
	}
	if _, err := c.Models(ctx); err != nil {
		return provider.Model{}, err
	}
	if m, ok := get(); ok {
		return m, nil
	}
	return provider.Model{}, fmt.Errorf("model %q is unavailable for this plugin account", id)
}
func (c *Client) PrepareInference(ctx context.Context, r provider.Request, m provider.Model, encoded []byte, _ bool) (*http.Request, error) {
	return c.prepareInference(ctx, r, m.Endpoint, encoded, nil)
}

func (c *Client) prepareInference(ctx context.Context, r provider.Request, endpoint string, encoded []byte, incoming http.Header) (*http.Request, error) {
	if !provider.InferencePath(endpoint) {
		return nil, errors.New("unsupported inference endpoint")
	}
	getURL := func() string { c.mu.RLock(); defer c.mu.RUnlock(); return c.catalogURL }
	base := getURL()
	if base == "" {
		if _, err := c.Models(ctx); err != nil {
			return nil, err
		}
		base = getURL()
	}
	params := newParams()
	params.CatalogURL, params.Endpoint, params.ConversationID = base, endpoint, r.ConversationID
	if params.ConversationID == "" {
		params.ConversationID = provider.RequestID()
	}
	params.Agent = r.Background || (len(r.Messages) > 0 && r.Messages[len(r.Messages)-1].Role != "user")
	hop := connectionHeaders(incoming)
	params.RequestHeaders = pluginRequestHeaders(incoming, hop)
	var spec requestSpec
	if err := c.plugin.call(ctx, "inference", params, &spec); err != nil {
		return nil, err
	}
	req, err := c.request(ctx, http.MethodPost, spec, encoded)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(endpoint, "/v1/messages") && req.Header.Get("Anthropic-Version") == "" {
		req.Header.Set("Anthropic-Version", "2023-06-01")
	}
	provider.CopyProtocolHeaders(req.Header, params.RequestHeaders)
	switch endpoint {
	case "/responses/compact", "/responses/input_tokens", "/v1/messages/count_tokens":
		req.Header.Set("Accept", "application/json")
	}
	if err := applyForwardHeaders(req.Header, spec.ForwardHeaders, hop); err != nil {
		return nil, err
	}
	return req, nil
}

func (c *Client) Forward(ctx context.Context, r provider.RawRequest) (*http.Response, error) {
	req, err := c.prepareInference(ctx, provider.Request{ConversationID: r.ConversationID, Background: r.AgentInitiated}, r.Endpoint, r.Body, r.Header)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = r.RawQuery
	return c.Do(req)
}
