// Package store persists application preferences and conversations. Provider
// credentials belong in the operating system credential store, never State.
package store

import (
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
	"github.com/fondoger/copilotchat/internal/provider/github"
	"github.com/fondoger/copilotchat/internal/provider/microsoft"
)

const (
	CurrentSchemaVersion               = 2
	DefaultProviderID                  = "github.com"
	ProviderKindGitHub                 = "github-copilot"
	ProviderKindM365                   = "m365-copilot"
	M365ProviderHost                   = microsoft.Host
	DefaultUpdateIntervalHours         = 24
	DefaultSidebarWidth        float32 = 240
	MinSidebarWidth            float32 = 240
	MaxSidebarWidth            float32 = 500
)

type Settings struct {
	Theme                string          `json:"theme"`
	FontSize             int             `json:"fontSize"`
	SidebarWidth         float32         `json:"sidebarWidth"`
	SendWithEnter        bool            `json:"sendWithEnter"`
	StayInTray           bool            `json:"stayInTray"`
	LocalForwarding      bool            `json:"localForwarding"`
	LocalPort            int             `json:"localPort"`
	LocalKey             string          `json:"localKey,omitempty"` // Local gateway key only; never an upstream credential.
	AutomaticUpdates     bool            `json:"automaticUpdates"`
	UpdateIntervalHours  int             `json:"updateIntervalHours"`
	LastUpdateCheck      time.Time       `json:"lastUpdateCheck,omitempty"` // Last successful check, not an attempt.
	ChatTitle            bool            `json:"chatTitle"`
	TitleModel           string          `json:"titleModel,omitempty"`
	TitleProviderID      string          `json:"titleProviderId,omitempty"`
	Model                string          `json:"model,omitempty"`
	ReasoningEffort      string          `json:"reasoningEffort,omitempty"`
	ThinkingBudgetTokens int             `json:"thinkingBudgetTokens,omitempty"`
	ActiveProviderID     string          `json:"activeProviderId"`
	Providers            []Provider      `json:"providers"`
	CollapsedGroups      map[string]bool `json:"collapsedGroups,omitempty"`
	MCPServers           []mcp.Config    `json:"mcpServers,omitempty"`
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
	CreatedAt        time.Time         `json:"createdAt,omitempty"`
	ProviderID       string            `json:"providerId,omitempty"`
	Model            string            `json:"model,omitempty"`
	Reasoning        string            `json:"reasoning,omitempty"`
	Stats            *MessageStats     `json:"stats,omitempty"`
	ID               string            `json:"id"`
	Role             string            `json:"role"`
	Content          string            `json:"content"`
	Attachments      []Attachment      `json:"attachments,omitempty"`
	Error            string            `json:"error,omitempty"`
	ProviderMessages []json.RawMessage `json:"providerMessages,omitempty"`
	ToolActivity     []string          `json:"toolActivity,omitempty"`
}

type Conversation struct {
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
	f, err := os.CreateTemp(s.dir, ".state-*.tmp")
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
	if err = replaceSnapshot(temp, s.path); err != nil {
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
			return fmt.Errorf("provider %q: unsupported provider kind %q", p.Name, p.Kind)
		}
		p.Name = strings.TrimSpace(p.Name)
		if p.Name == "" {
			p.Name = defaultName
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
