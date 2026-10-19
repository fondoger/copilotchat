// Package localproxy hosts the app's loopback-only agent API.
package localproxy

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

const DefaultPort = 39321
const DefaultKey = "copilotchat"
const MaxBody = 32 << 20

type Target struct {
	Client provider.RawClient
}

// Model is a public alias bound to exactly one account and upstream model.
type Model struct {
	ProviderID string
	Alias      string
	Model      provider.Model
}

type Profile struct {
	ID     string
	Models []Model
}

type profileEntry struct {
	Profile
	ctx    context.Context
	cancel context.CancelFunc
}

type entry struct {
	Target
	ctx    context.Context
	cancel context.CancelFunc
}

type Server struct {
	mu       sync.RWMutex
	targets  map[string]*entry
	profiles map[string]*profileEntry
	key      [32]byte
	http     *http.Server
	address  string
	err      error
	closed   bool
}

func New(key string) *Server {
	return &Server{targets: make(map[string]*entry), profiles: make(map[string]*profileEntry), key: sha256.Sum256([]byte(key))}
}

var profileNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

func ValidProfileName(name string) bool { return profileNamePattern.MatchString(name) }

func BaseURL(port int, profile string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/%s", port, url.PathEscape(profile))
}

// Update receives UI-owned snapshots. Replacing, disabling, signing out or
// deleting an account cancels its in-flight requests as well as future access.
func (s *Server) Update(targets map[string]Target, profiles map[string]Profile) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	for id, old := range s.targets {
		if next, ok := targets[id]; !ok || next.Client != old.Client {
			old.cancel()
			delete(s.targets, id)
		}
	}
	for id, target := range targets {
		if old := s.targets[id]; old != nil {
			old.Target = target
			continue
		}
		ctx, cancel := context.WithCancel(context.Background())
		s.targets[id] = &entry{Target: target, ctx: ctx, cancel: cancel}
	}
	for name, old := range s.profiles {
		if next, ok := profiles[name]; !ok || next.ID != old.ID {
			old.cancel()
			delete(s.profiles, name)
		}
	}
	for name, profile := range profiles {
		if !ValidProfileName(name) || profile.ID == "" {
			continue
		}
		// Ambiguous aliases fail closed, never pick an account by map order.
		counts := map[string]int{}
		for _, model := range profile.Models {
			counts[model.Alias]++
		}
		models := []Model{}
		for _, model := range profile.Models {
			if model.Alias != "" && model.Model.ID != "" && counts[model.Alias] == 1 && s.targets[model.ProviderID] != nil {
				model.Model.Endpoints = slices.Clone(model.Model.Endpoints)
				model.Model.ThinkingEfforts = slices.Clone(model.Model.ThinkingEfforts)
				models = append(models, model)
			}
		}
		slices.SortFunc(models, func(a, b Model) int { return cmp.Compare(a.Alias, b.Alias) })
		profile.Models = models
		if old := s.profiles[name]; old != nil {
			old.Profile = profile
		} else {
			ctx, cancel := context.WithCancel(context.Background())
			s.profiles[name] = &profileEntry{Profile: profile, ctx: ctx, cancel: cancel}
		}
	}
}

// Start also accepts port zero for isolated tests; the UI persists a fixed port.
func (s *Server) Start(port int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.http != nil {
		return errors.New("forwarding service already started or closed")
	}
	if port < 0 || port > 65535 {
		return errors.New("invalid local port")
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return fmt.Errorf("could not listen on local port %d: %w", port, err)
	}
	s.address = listener.Addr().String()
	s.http = &http.Server{Handler: s, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 60 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 32 << 10}
	go func() {
		err := s.http.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			s.mu.Lock()
			s.err = err
			s.mu.Unlock()
		}
	}()
	return nil
}

func (s *Server) Address() string { s.mu.RLock(); defer s.mu.RUnlock(); return s.address }
func (s *Server) Err() error      { s.mu.RLock(); defer s.mu.RUnlock(); return s.err }

func (s *Server) Close() {
	s.mu.Lock()
	s.closed = true
	for _, entry := range s.targets {
		entry.cancel()
	}
	s.targets = make(map[string]*entry)
	for _, profile := range s.profiles {
		profile.cancel()
	}
	s.profiles = make(map[string]*profileEntry)
	server := s.http
	s.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
}

func apiError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"type": "error", "error": map[string]string{"type": "invalid_request_error", "message": message}})
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	host, _, err := net.SplitHostPort(r.Host)
	if err != nil || host != "127.0.0.1" || r.URL.IsAbs() || r.Header.Get("Origin") != "" {
		apiError(w, http.StatusForbidden, "This endpoint accepts local agent requests only.")
		return
	}
	key := r.Header.Get("X-Api-Key")
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		key = strings.TrimPrefix(auth, "Bearer ")
	}
	hash := sha256.Sum256([]byte(key))
	if key == "" || subtle.ConstantTimeCompare(hash[:], s.key[:]) != 1 {
		apiError(w, http.StatusUnauthorized, "Invalid local API key.")
		return
	}
	parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
	if len(parts) != 2 || !ValidProfileName(parts[0]) {
		apiError(w, 404, "Use the profile base URL from Agent setup.")
		return
	}
	profileName, endpoint := parts[0], "/"+parts[1]
	s.mu.RLock()
	profile := s.profiles[profileName]
	var models []Model
	var profileCtx context.Context
	var profileID string
	if profile != nil {
		models, profileCtx, profileID = slices.Clone(profile.Models), profile.ctx, profile.ID
	}
	s.mu.RUnlock()
	if profile == nil {
		apiError(w, 404, "Profile does not exist. Check Agent setup in Copilot Chat.")
		return
	}
	if endpoint == "/models" || endpoint == "/v1/models" || endpoint == "/codex/models" {
		if r.Method != http.MethodGet {
			apiError(w, 405, "Use GET for models.")
			return
		}
		if endpoint == "/codex/models" {
			writeCodexModels(w, models)
		} else {
			writeModels(w, models, endpoint == "/v1/models")
		}
		return
	}
	if endpoint == "/v1/responses" || endpoint == "/v1/chat/completions" {
		endpoint = strings.TrimPrefix(endpoint, "/v1")
	}
	if !provider.InferencePath(endpoint) {
		apiError(w, 404, "Unsupported API endpoint.")
		return
	}
	if r.Method != http.MethodPost {
		apiError(w, 405, "Use POST for inference.")
		return
	}
	if r.Header.Get("Content-Encoding") != "" {
		apiError(w, 415, "Compressed request bodies are not supported.")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBody))
	if err != nil {
		apiError(w, 413, "Request body exceeds 32 MiB or could not be read.")
		return
	}
	var envelope struct {
		Model    string          `json:"model"`
		Input    json.RawMessage `json:"input"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if json.Unmarshal(body, &envelope) != nil || envelope.Model == "" {
		apiError(w, 400, "A JSON request with a model ID is required.")
		return
	}
	protocol := strings.TrimSuffix(endpoint, "/count_tokens")
	s.mu.RLock()
	var model Model
	var target Target
	var providerCtx context.Context
	// Re-read after receiving the body so a removed/renamed profile or model
	// cannot start a new request using an obsolete snapshot.
	models = nil
	if current := s.profiles[profileName]; current != nil && current.ID == profileID {
		models = current.Models
	}
	for _, candidate := range models {
		if candidate.Alias == envelope.Model && supports(candidate.Model, protocol) {
			if item := s.targets[candidate.ProviderID]; item != nil {
				model, target, providerCtx = candidate, item.Target, item.ctx
			}
			break
		}
	}
	s.mu.RUnlock()
	if providerCtx == nil {
		apiError(w, 400, "Model alias is disabled, unavailable, or does not support this API. Check Agent setup in Copilot Chat.")
		return
	}
	if model.Alias != model.Model.ID {
		// Only the routing field changes. RawMessage retains unknown fields,
		// numeric precision, cache markers, tool arguments and thinking signatures.
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(body, &fields)
		fields["model"], _ = json.Marshal(model.Model.ID)
		body, _ = json.Marshal(fields)
	}
	agent := false
	vision := hasImages(envelope.Input)
	for _, message := range envelope.Messages {
		vision = vision || hasImages(message.Content)
	}
	if len(envelope.Messages) > 0 {
		last := envelope.Messages[len(envelope.Messages)-1]
		agent = last.Role != "user"
		var blocks []struct {
			Type string `json:"type"`
		}
		_ = json.Unmarshal(last.Content, &blocks)
		for _, block := range blocks {
			if block.Type == "tool_result" {
				agent = true
			}
		}
	}
	var input []struct {
		Type string `json:"type"`
		Role string `json:"role"`
	}
	if json.Unmarshal(envelope.Input, &input) == nil && len(input) > 0 {
		last := input[len(input)-1]
		agent = last.Type == "function_call_output" || last.Role == "assistant" || last.Role == "tool"
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	stop := context.AfterFunc(providerCtx, cancel)
	defer stop()
	stopProfile := context.AfterFunc(profileCtx, cancel)
	defer stopProfile()
	// Remove the upload deadline before waiting on a long-running model stream.
	_ = http.NewResponseController(w).SetReadDeadline(time.Time{})
	query := url.Values{}
	if beta := r.URL.Query().Get("beta"); beta == "true" {
		query.Set("beta", beta)
	}
	response, err := target.Client.Forward(ctx, provider.RawRequest{Endpoint: endpoint, RawQuery: query.Encode(), Body: body, Header: r.Header, ConversationID: conversationID(profileID+":"+model.ProviderID, r.Header), AgentInitiated: agent, Vision: vision})
	if err != nil {
		apiError(w, 502, "Upstream request failed. Check the selected provider's sign-in and connection in Copilot Chat.")
		return
	}
	defer response.Body.Close()
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		apiError(w, 502, "The provider returned an unsupported redirect.")
		return
	}
	for key, values := range response.Header {
		lower := strings.ToLower(key)
		if lower == "content-type" || lower == "retry-after" || lower == "request-id" || lower == "x-request-id" || strings.HasPrefix(lower, "anthropic-ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-") {
			w.Header()[key] = slices.Clone(values)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(response.StatusCode)
	controller := http.NewResponseController(w)
	_ = controller.Flush()
	buffer := make([]byte, 32<<10)
	for {
		n, readErr := response.Body.Read(buffer)
		if n > 0 {
			if _, err := w.Write(buffer[:n]); err != nil {
				return
			}
			if err := controller.Flush(); err != nil {
				return
			}
		}
		if readErr != nil {
			return
		}
	}
}

func supports(m provider.Model, endpoint string) bool {
	if len(m.Endpoints) != 0 {
		return slices.Contains(m.Endpoints, endpoint)
	}
	return m.Endpoint == endpoint
}

func hasImages(content json.RawMessage) bool {
	var blocks []struct {
		Type    string          `json:"type"`
		Content json.RawMessage `json:"content"`
	}
	if json.Unmarshal(content, &blocks) != nil {
		return false
	}
	for _, block := range blocks {
		if block.Type == "image" || block.Type == "image_url" || block.Type == "input_image" || hasImages(block.Content) {
			return true
		}
	}
	return false
}

func conversationID(id string, h http.Header) string {
	// Current Codex sends thread-id and session-id; older releases used session_id.
	// Prefer the thread identity over its potentially shared cache-affinity ID.
	for _, key := range []string{"X-Claude-Code-Session-Id", "Thread-Id", "Session-Id", "Session_id", "X-ConversationId"} {
		if value := strings.TrimSpace(h.Get(key)); value != "" {
			return provider.ConversationID(id+":"+strings.ToLower(key), value)
		}
	}
	// No shared global fallback: clients without a session identifier represent
	// independent requests. Such clients can supply X-ConversationId explicitly.
	return provider.RequestID()
}

func writeModels(w http.ResponseWriter, models []Model, messages bool) {
	data := []map[string]any{}
	for _, model := range models {
		if messages && !supports(model.Model, "/v1/messages") {
			continue
		}
		data = append(data, map[string]any{"id": model.Alias, "type": "model", "object": "model", "display_name": model.Alias, "name": model.Alias, "owned_by": "copilot-chat"})
	}
	result := map[string]any{"data": data, "object": "list", "has_more": false, "first_id": nil, "last_id": nil}
	if len(data) > 0 {
		result["first_id"], result["last_id"] = data[0]["id"], data[len(data)-1]["id"]
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(result)
}
