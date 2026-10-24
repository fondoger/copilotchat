package store

import (
	"net/url"
	"strings"
)

// DoubaoConversationURL accepts only resumable conversations on the fixed site.
// IDs stay strings: Doubao IDs may exceed JavaScript's safe integer range.
func DoubaoConversationURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != DoubaoHost || u.User != nil || u.Opaque != "" || u.RawPath != "" {
		return "", false
	}
	id, ok := strings.CutPrefix(u.Path, "/chat/")
	if !ok || id == "" {
		return "", false
	}
	for _, ch := range id {
		if ch < '0' || ch > '9' {
			return "", false
		}
	}
	return DoubaoChatURL + "/" + id, true
}
