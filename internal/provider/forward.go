package provider

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"fmt"
	"net/http"
)

// RawClient preserves an agent's protocol body and streaming response. It must
// authorize only known inference paths on its own account's service.
type RawClient interface {
	Forward(context.Context, RawRequest) (*http.Response, error)
}

type RawRequest struct {
	Endpoint, ConversationID string
	RawQuery                 string
	Body                     []byte
	Header                   http.Header
	AgentInitiated, Vision   bool
}

func InferencePath(path string) bool {
	return path == "/v1/messages" || path == "/v1/messages/count_tokens" || path == "/responses" || path == "/chat/completions"
}

// CopyProtocolHeaders carries protocol extensions, never agent credentials,
// arbitrary routing headers, or hop-by-hop transport metadata.
func CopyProtocolHeaders(dst, src http.Header) {
	for _, key := range []string{"Anthropic-Version", "Anthropic-Beta", "OpenAI-Beta"} {
		if value := src.Get(key); value != "" {
			dst.Set(key, value)
		}
	}
}

func RequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return formatUUID(b[:])
}

// ConversationID uses an application-owned UUIDv5 namespace. This reproduces
// the CLI's observed stable-per-conversation lifetime, not its private hash recipe.
func ConversationID(scope, id string) string {
	h := sha1.New()
	h.Write([]byte{0x0d, 0xaf, 0x15, 0x71, 0x0a, 0xaa, 0x47, 0xf2, 0xa1, 0x96, 0x48, 0x6b, 0x18, 0xfa, 0x21, 0x59})
	h.Write([]byte(scope + "\x00" + id))
	b := h.Sum(nil)[:16]
	b[6] = (b[6] & 15) | 80
	b[8] = (b[8] & 63) | 128
	return formatUUID(b)
}

func formatUUID(b []byte) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}
