package wam

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// Profile is a host grant: an application registration owned by the host
// operator, its API audience, and the exact origins allowed to receive tokens.
// Never construct one from a plugin manifest or plugin-supplied arguments.
type Profile struct {
	ClientID string   `json:"clientId"`
	Resource string   `json:"resource"`
	Origins  []string `json:"origins"`
}

const profilesEnv = "COPILOT_CHAT_WAM_PROFILES"
const configHelp = "set " + profilesEnv + " to the absolute path of your JSON file mapping profile names to {clientId, resource, origins}; use your own Entra application registration"

var guidPattern = regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
var hostLabel = regexp.MustCompile(`^[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

func validID(id string) bool {
	return guidPattern.MatchString(id) && id != "00000000-0000-0000-0000-000000000000"
}

func validOrigin(origin string) bool {
	// Accept only a bare HTTPS authority, not a URL to normalize. This also
	// rejects empty ports, empty query/fragment markers, and escaped hosts.
	host, ok := strings.CutPrefix(origin, "https://")
	if !ok || host == "" || len(host) > 253 {
		return false
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		ip, err := netip.ParseAddr(host[1 : len(host)-1])
		return err == nil && ip.Is6() && ip.Zone() == ""
	}
	for label := range strings.SplitSeq(host, ".") {
		if !hostLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func (p Profile) Validate() error {
	if !validID(p.ClientID) || !validID(p.Resource) {
		return errors.New("WAM clientId and resource must both be configured, nonzero GUIDs")
	}
	if len(p.Origins) == 0 {
		return errors.New("WAM profile needs at least one HTTPS origin")
	}
	for i, origin := range p.Origins {
		if !validOrigin(origin) {
			return fmt.Errorf("WAM origins[%d] must be an exact https://host origin without port, userinfo, path, query, fragment, or wildcard", i)
		}
	}
	return nil
}

type registration struct{ clientID, resource string }

func (p Profile) registration() registration { return registration{p.ClientID, p.Resource} }
func (r registration) scope() string         { return "api://" + r.resource + "/.default" }

func (p Profile) hash() string {
	// The validated profile contains only ASCII strings and no maps. Hash its
	// complete representation so adding even one origin requires new consent.
	data, _ := json.Marshal(p)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// ReadProfile resolves only the named host grant. The caller supplies a name,
// never a filesystem path. No file, registration or resource is provisioned.
func ReadProfile(name string) (Profile, error) {
	path := os.Getenv(profilesEnv)
	if !filepath.IsAbs(path) {
		return Profile{}, errors.New(configHelp)
	}
	if strings.TrimSpace(name) == "" {
		return Profile{}, fmt.Errorf("missing WAM profile name; %s", configHelp)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Profile{}, fmt.Errorf("read WAM profiles: %w; %s", err, configHelp)
	}
	var profiles map[string]Profile
	if err := json.Unmarshal(data, &profiles); err != nil {
		return Profile{}, fmt.Errorf("decode WAM profiles: %w; %s", err, configHelp)
	}
	p, ok := profiles[name]
	if !ok {
		return Profile{}, fmt.Errorf("WAM profile %q is not configured; %s", name, configHelp)
	}
	if err := p.Validate(); err != nil {
		return Profile{}, fmt.Errorf("WAM profile %q: %w; %s", name, err, configHelp)
	}
	return p, nil
}
