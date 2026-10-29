// Package store persists application preferences and conversations. Provider
// credentials belong in the operating system credential store, never State.
package store

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fondoger/copilotchat/internal/mcp"
	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
)

const (
	CurrentSchemaVersion               = 2
	DefaultProviderID                  = "github.com"
	ProviderKindGitHub                 = "github-copilot"
	ProviderKindM365                   = "m365-copilot"
	ProviderKindDoubao                 = "doubao-web"
	ProviderKindDeepSeekWeb            = "deepseek-web"
	ProviderKindClaudeWeb              = "claude-web"
	ProviderKindGeminiWeb              = "gemini-web"
	ProviderKindChatGPTWeb             = "chatgpt-web"
	DoubaoHost                         = "www.doubao.com"
	DoubaoChatURL                      = "https://www.doubao.com/chat"
	M365ProviderHost                   = microsoft.Host
	DefaultUpdateIntervalHours         = 24
	DefaultSidebarWidth        float32 = 240
	MinSidebarWidth            float32 = 240
	MaxSidebarWidth            float32 = 500
)

type Settings struct {
	Theme                string           `json:"theme"`
	FontSize             int              `json:"fontSize"`
	SidebarWidth         float32          `json:"sidebarWidth"`
	SendWithEnter        bool             `json:"sendWithEnter"`
	StayInTray           bool             `json:"stayInTray"`
	LocalForwarding      bool             `json:"localForwarding"`
	GatewayInspection    bool             `json:"gatewayInspection"`
	LocalPort            int              `json:"localPort"`
	LocalKey             string           `json:"localKey,omitempty"` // Local gateway key only; never an upstream credential.
	ForwardProfiles      []ForwardProfile `json:"forwardProfiles"`
	AutomaticUpdates     bool             `json:"automaticUpdates"`
	UpdateChannel        string           `json:"updateChannel,omitempty"`
	PendingUpdate        *PendingUpdate   `json:"pendingUpdate,omitempty"`
	UpdateIntervalHours  int              `json:"updateIntervalHours"`
	LastUpdateCheck      time.Time        `json:"lastUpdateCheck,omitempty"` // Last successful check, not an attempt.
	ChatTitle            bool             `json:"chatTitle"`
	TitleModel           string           `json:"titleModel,omitempty"`
	TitleProviderID      string           `json:"titleProviderId,omitempty"`
	Model                string           `json:"model,omitempty"`
	ReasoningEffort      string           `json:"reasoningEffort,omitempty"`
	ThinkingBudgetTokens int              `json:"thinkingBudgetTokens,omitempty"`
	ActiveProviderID     string           `json:"activeProviderId"`
	Providers            []Provider       `json:"providers"`
	CollapsedGroups      map[string]bool  `json:"collapsedGroups,omitempty"`
	MCPServers           []mcp.Config     `json:"mcpServers,omitempty"`
}

type PendingUpdate struct {
	Channel string `json:"channel"`
	Version string `json:"version"`
}

// Enabled is independent of the native chat picker. Public gateway IDs are
// generated from the provider and upstream ID; old JSON alias fields are ignored.
type ForwardModel struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
	Enabled    bool   `json:"enabled"`
}

// Agent choices refer to the original model and account, not its public ID.
type ForwardModelRef struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

// ID remains stable across renames; Name is the profile's public URL segment.
type ForwardProfile struct {
	ID                    string                     `json:"id"`
	Name                  string                     `json:"name"`
	Models                []ForwardModel             `json:"models"`
	Agent                 string                     `json:"agent,omitempty"`
	ClaudeDefaults        map[string]ForwardModelRef `json:"claudeDefaults,omitempty"`
	ClaudePermissionMode  string                     `json:"claudePermissionMode,omitempty"`
	ClaudeClassifierModel ForwardModelRef            `json:"claudeCodeClassifierModel,omitempty"`
	CodexModel            ForwardModelRef            `json:"codexModel,omitempty"`
	CodexRemoteCompaction bool                       `json:"codexRemoteCompaction,omitempty"`
}

type Provider struct {
	Disabled          bool      `json:"disabled,omitempty"`
	ModelsRefreshedAt time.Time `json:"modelsRefreshedAt,omitempty"`
	ID                string    `json:"id"`
	Kind              string    `json:"kind"`
	Name              string    `json:"name"`
	Host              string    `json:"host"`
	Login             string    `json:"login,omitempty"`
	DisabledModels    []string  `json:"disabledModels,omitempty"`
	PinnedModels      []string  `json:"pinnedModels,omitempty"`
}

func DefaultProvider() Provider {
	return Provider{ID: DefaultProviderID, Kind: ProviderKindGitHub, Name: "GitHub Copilot", Host: "github.com"}
}

type MessageStats struct {
	InputTokens          int   `json:"inputTokens,omitempty"`
	OutputTokens         int   `json:"outputTokens,omitempty"`
	CachedInputTokens    int   `json:"cachedInputTokens,omitempty"`
	UsageAvailable       bool  `json:"usageAvailable,omitempty"`
	InputAvailable       bool  `json:"inputAvailable,omitempty"`
	OutputAvailable      bool  `json:"outputAvailable,omitempty"`
	CachedInputAvailable bool  `json:"cachedInputAvailable,omitempty"`
	TimeToFirstTokenMS   int64 `json:"timeToFirstTokenMs,omitempty"`
	DurationMS           int64 `json:"durationMs,omitempty"`
	GenerationMS         int64 `json:"generationMs,omitempty"`
}

type Attachment struct {
	Name string `json:"name"`
	MIME string `json:"mime"`
	Data string `json:"data"` // Base64-encoded attachment bytes.
	Size int64  `json:"size"`
}

type Message struct {
	CreatedAt        time.Time           `json:"createdAt,omitempty"`
	ProviderID       string              `json:"providerId,omitempty"`
	Model            string              `json:"model,omitempty"`
	Reasoning        string              `json:"reasoning,omitempty"`
	Stats            *MessageStats       `json:"stats,omitempty"`
	ID               string              `json:"id"`
	Role             string              `json:"role"`
	Content          string              `json:"content"`
	Attachments      []Attachment        `json:"attachments,omitempty"`
	Error            string              `json:"error,omitempty"`
	ProviderMessages []json.RawMessage   `json:"providerMessages,omitempty"`
	ToolActivity     []string            `json:"toolActivity,omitempty"`
	Citations        []provider.Citation `json:"citations,omitempty"`
}

type Conversation struct {
	WebKind              string    `json:"webKind,omitempty"`
	WebURL               string    `json:"webUrl,omitempty"`
	WebStarted           bool      `json:"webStarted,omitempty"`
	NativeSearch         bool      `json:"nativeSearch,omitempty"`
	TitleManual          bool      `json:"titleManual,omitempty"`
	ProviderID           string    `json:"providerId"`
	ReasoningEffort      string    `json:"reasoningEffort,omitempty"`
	ThinkingBudgetTokens int       `json:"thinkingBudgetTokens,omitempty"`
	ID                   string    `json:"id"`
	Title                string    `json:"title"`
	Model                string    `json:"model,omitempty"`
	Favorite             bool      `json:"favorite,omitempty"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
	Messages             []Message `json:"messages"`
}

type State struct {
	SchemaVersion int            `json:"schemaVersion"`
	Settings      Settings       `json:"settings"`
	Conversations []Conversation `json:"conversations"`
	ActiveID      string         `json:"activeId,omitempty"`
}

func DefaultState() State {
	return State{
		SchemaVersion: CurrentSchemaVersion,
		Settings: Settings{
			Theme: "system", FontSize: 16, SidebarWidth: DefaultSidebarWidth, SendWithEnter: true, ChatTitle: true,
			AutomaticUpdates: true, UpdateIntervalHours: DefaultUpdateIntervalHours,
			LocalPort:        39321,
			ForwardProfiles:  []ForwardProfile{{ID: "default", Name: "default"}},
			ActiveProviderID: DefaultProviderID, Providers: []Provider{DefaultProvider()},
		},
		Conversations: []Conversation{},
	}
}

type Store struct {
	dir  string
	path string
	mu   sync.Mutex
}

func New(dir string) (*Store, error) {
	if dir == "" {
		return nil, errors.New("state directory must not be empty")
	}
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	dir = absolute
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	return &Store{dir: dir, path: filepath.Join(dir, "state.json")}, nil
}

// Load returns defaults on the first launch. It reports malformed state instead
// of silently discarding conversations. Missing settings retain their defaults.
func (s *Store) Load() (State, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	state := DefaultState()
	f, err := openSnapshot(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return state, nil
	}
	if err != nil {
		return state, fmt.Errorf("open state: %w", err)
	}
	defer f.Close()
	// Absence of a version identifies the original schema, even though the
	// remaining fields are decoded over defaults for newly added preferences.
	state.SchemaVersion = 0
	decoder := json.NewDecoder(f)
	if err := decoder.Decode(&state); err != nil {
		return DefaultState(), fmt.Errorf("read state: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			err = errors.New("unexpected additional JSON value")
		}
		return DefaultState(), fmt.Errorf("read state: %w", err)
	}
	normalize(&state)
	if err := validateProviders(&state); err != nil {
		return DefaultState(), err
	}
	return state, nil
}

// Save commits a complete snapshot with a same-directory atomic rename. A
// failed write leaves the previous snapshot intact. The caller owns State and
// must avoid mutating it concurrently with Save.
func (s *Store) Save(state State) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	normalize(&state)
	if err := validateProviders(&state); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	return writeSnapshot(s.dir, s.path, data)
}

// AgentCatalogPath is stable across profile renames and safe for arbitrary IDs.
func (s *Store) AgentCatalogPath(profileID string) string {
	return filepath.Join(s.dir, "agent-catalogs", fmt.Sprintf("%x.json", sha256.Sum256([]byte(profileID))))
}

func (s *Store) SaveAgentCatalog(profileID string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path := s.AgentCatalogPath(profileID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return writeSnapshot(filepath.Dir(path), path, data)
}

func writeSnapshot(dir, path string, data []byte) error {
	f, err := os.CreateTemp(dir, ".snapshot-*.tmp")
	if err != nil {
		return fmt.Errorf("create state snapshot: %w", err)
	}
	temp := f.Name()
	defer os.Remove(temp)
	defer f.Close()
	if _, err = f.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("write state snapshot: %w", err)
	}
	if err = f.Sync(); err != nil {
		return fmt.Errorf("sync state snapshot: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close state snapshot: %w", err)
	}
	if err = replaceSnapshot(temp, path); err != nil {
		return fmt.Errorf("replace state snapshot: %w", err)
	}
	return nil
}

func normalize(state *State) {
	if state.SchemaVersion < 1 {
		// Schema 0 used 15 as its default chat size. Only that legacy value is
		// migrated; explicitly choosing 15 in schema 1 remains supported.
		if state.Settings.FontSize == 15 {
			state.Settings.FontSize = 16
		}
	}
	if state.SchemaVersion < 2 && state.Settings.TitleModel != "" && state.Settings.TitleProviderID == "" {
		state.Settings.TitleProviderID = DefaultProviderID
	}
	if state.SchemaVersion < CurrentSchemaVersion {
		state.SchemaVersion = CurrentSchemaVersion
	}
	if state.Settings.Providers == nil {
		// Load decodes missing fields over DefaultState; an explicit empty list
		// means the user removed every provider and must stay empty.
		state.Settings.Providers = []Provider{}
	}
	if state.Settings.ActiveProviderID == "" && len(state.Settings.Providers) > 0 {
		for _, p := range state.Settings.Providers {
			if !p.Disabled {
				state.Settings.ActiveProviderID = p.ID
				break
			}
		}
	}
	for i := range state.Conversations {
		if state.Conversations[i].ProviderID == "" {
			state.Conversations[i].ProviderID = DefaultProviderID
		}
	}
	for i := range state.Settings.Providers {
		if state.Settings.Providers[i].Kind == "" {
			// Existing providers all used GitHub Copilot. Never infer provider
			// type from a host, so credentials cannot cross auth boundaries.
			state.Settings.Providers[i].Kind = ProviderKindGitHub
		}
	}
	switch state.Settings.Theme {
	case "system", "light", "dark":
	default:
		state.Settings.Theme = "system"
	}
	if state.Settings.FontSize < 10 || state.Settings.FontSize > 32 {
		state.Settings.FontSize = 16
	}
	switch state.Settings.UpdateIntervalHours {
	case 1, 6, 12, 24, 168:
	default:
		state.Settings.UpdateIntervalHours = DefaultUpdateIntervalHours
	}
	state.Settings.SidebarWidth = ClampSidebarWidth(state.Settings.SidebarWidth)
	switch state.Settings.UpdateChannel {
	case "", "stable", "dev":
	default:
		state.Settings.UpdateChannel = ""
	}
	if p := state.Settings.PendingUpdate; p != nil && (p.Version == "" || (p.Channel != "stable" && p.Channel != "dev")) {
		state.Settings.PendingUpdate = nil
	}
	if state.Conversations == nil {
		state.Conversations = []Conversation{}
	}
}

func validateProviders(state *State) error {
	seen := map[string]bool{}
	for i := range state.Settings.Providers {
		p := &state.Settings.Providers[i]
		if p.ID == "" || seen[p.ID] {
			return fmt.Errorf("provider IDs must be nonempty and unique")
		}
		seen[p.ID] = true
		defaultName := "GitHub Copilot"
		switch p.Kind {
		case ProviderKindGitHub:
			host, err := github.NormalizeHost(p.Host)
			if err != nil {
				return fmt.Errorf("provider %q: %w", p.Name, err)
			}
			p.Host = host
		case ProviderKindM365:
			host := strings.ToLower(strings.TrimSpace(p.Host))
			if host != "" && host != M365ProviderHost {
				return fmt.Errorf("provider %q: Microsoft 365 Copilot must use %s", p.Name, M365ProviderHost)
			}
			p.Host = M365ProviderHost
			defaultName = "Microsoft 365 Copilot"
		default:
			web, ok := WebProviderFor(p.Kind)
			if !ok {
				return fmt.Errorf("provider %q: unsupported provider kind %q", p.Name, p.Kind)
			}
			if p.Host != "" && p.Host != web.Host {
				return fmt.Errorf("provider %q: %s must use %s", p.Name, web.Name, web.Host)
			}
			p.Host = web.Host
			defaultName = web.Name
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = defaultName
		}
	}
	for i := range state.Conversations {
		chat := &state.Conversations[i]
		if chat.WebKind != "" && !IsWebProvider(chat.WebKind) {
			return fmt.Errorf("conversation %q: unsupported web provider kind", chat.ID)
		}
		if chat.WebURL != "" {
			canonical, ok := WebConversationURL(chat.WebKind, chat.WebURL)
			if !ok {
				return fmt.Errorf("conversation %q: invalid web conversation URL", chat.ID)
			}
			chat.WebURL = canonical
		}
	}
	if !seen[state.Settings.ActiveProviderID] {
		state.Settings.ActiveProviderID = ""
		for _, p := range state.Settings.Providers {
			if !p.Disabled {
				state.Settings.ActiveProviderID = p.ID
				break
			}
		}
	}
	if len(state.Settings.Providers) == 0 {
		state.Settings.Model, state.Settings.ReasoningEffort = "", ""
		state.Settings.ThinkingBudgetTokens = 0
		state.Settings.TitleProviderID, state.Settings.TitleModel = "", ""
	}
	return nil
}

// ClampSidebarWidth validates a persisted sidebar preference in logical pixels.
// Window-dependent constraints are applied by the view without changing it.
func ClampSidebarWidth(width float32) float32 {
	if math.IsNaN(float64(width)) || math.IsInf(float64(width), 0) {
		return DefaultSidebarWidth
	}
	return min(MaxSidebarWidth, max(MinSidebarWidth, width))
}
