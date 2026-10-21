package localproxy

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

// ProviderIdentity is ordered by the persisted provider list, never a map.
// Count configured accounts, including temporarily offline ones, so refreshing
// credentials or hiding models does not rename another account's public IDs.
type ProviderIdentity struct {
	ID, Kind, Name, Login string
}

// Model binds generated public IDs to one account and its original model.
// Routes contains only generated short/qualified IDs, never saved user aliases.
type Model struct {
	ProviderID, ID, ProviderName string
	Routes                       []string
	Model                        provider.Model
}

func accountSegment(p ProviderIdentity) string {
	login := strings.TrimSpace(p.Login)
	if login == "" {
		return fmt.Sprintf("account-%x", sha256.Sum256([]byte(p.ID)))[:20]
	}
	return url.PathEscape(login)
}

// NameModels also orders models by saved provider priority. The short route is
// intentionally shared: the server chooses the first available matching account.
func NameModels(models []Model, providers []ProviderIdentity) []Model {
	counts, logins := map[string]int{}, map[string]int{}
	for _, p := range providers {
		counts[p.Kind]++
		logins[p.Kind+"/"+accountSegment(p)]++
	}
	var result []Model
	for _, p := range providers {
		account := accountSegment(p)
		qualified := account
		if logins[p.Kind+"/"+account] > 1 {
			qualified += fmt.Sprintf("-%x", sha256.Sum256([]byte(p.ID)))[:9]
		}
		for _, m := range models {
			if m.ProviderID != p.ID || m.Model.ID == "" {
				continue
			}
			short := p.Kind + "/" + m.Model.ID
			full := p.Kind + "/" + qualified + "/" + m.Model.ID
			m.ID, m.ProviderName = short, p.Name
			if counts[p.Kind] > 1 {
				m.ID = full
			}
			m.Routes = []string{short, full, p.Kind + "/" + account + "/" + m.Model.ID}
			result = append(result, m)
		}
	}
	return result
}

func supportsMessages(m provider.Model) bool {
	return supports(m, "/v1/messages") || supports(m, "/responses")
}
