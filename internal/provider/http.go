package provider

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/doganarif/govisual/v2"
)

const Version = "0.5.9"
const UserAgent = "CopilotChatNative/0.5.9"

// NewHTTPClient never follows redirects carrying account credentials.
func NewHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 30 * time.Second
	return &http.Client{Transport: govisual.WrapTransport(transport,
		govisual.WithTransportBodyLogging(true),
		govisual.WithTransportMaxBodyBytes(1<<20),
	), CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		// Never forward a credential-bearing request to a redirect destination.
		return http.ErrUseLastResponse
	}}
}

// ReadJSON reads a bounded JSON response and closes its body.
func ReadJSON(resp *http.Response, target any) error {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ResponseError(resp)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("invalid Copilot response: %w", err)
	}
	return nil
}

func ResponseError(resp *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<10))
	message := strings.TrimSpace(string(data))
	var detail struct {
		Message string          `json:"message"`
		Error   json.RawMessage `json:"error"`
	}
	if json.Unmarshal(data, &detail) == nil {
		if detail.Message != "" {
			message = detail.Message
		} else if len(detail.Error) != 0 {
			var nested struct {
				Message string `json:"message"`
			}
			var plain string
			if json.Unmarshal(detail.Error, &nested) == nil && nested.Message != "" {
				message = nested.Message
			} else if json.Unmarshal(detail.Error, &plain) == nil {
				message = plain
			}
		}
	}
	if message == "" {
		message = http.StatusText(resp.StatusCode)
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	return &HTTPError{StatusCode: resp.StatusCode, Message: message}
}
