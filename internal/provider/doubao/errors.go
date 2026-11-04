package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

const RecoveryURL = "copilotchat://providers/doubao/open"

// Only locally constructed errors may carry recovery links. Raw transport and
// upstream error text can contain credentials and never reach the native UI.
type safeError struct{ *provider.HTTPError }

func (e *safeError) Unwrap() error { return e.HTTPError }

func newWebError(status int, message string) *safeError {
	return &safeError{&provider.HTTPError{StatusCode: status, Message: message}}
}

func LoginRequired() error {
	return newWebError(http.StatusUnauthorized, "Doubao web sign-in is required. [Open Doubao]("+RecoveryURL+")")
}

func verificationRequired() error {
	return newWebError(http.StatusServiceUnavailable, "Complete verification in Doubao web. [Open Doubao]("+RecoveryURL+")")
}

func rateLimited() error {
	return newWebError(http.StatusTooManyRequests, "Doubao is rate limited; try again later")
}

func webRejected() error {
	return newWebError(http.StatusServiceUnavailable, "Doubao web rejected this request. [Open Doubao]("+RecoveryURL+") to check sign-in or verification, then retry.")
}

func protocolError() *safeError {
	return newWebError(http.StatusBadGateway, "Doubao returned an unsupported response")
}

// SessionUnavailable reports local browser/transport failures without exposing
// page errors, signed URLs, or credentials to the native chat.
func SessionUnavailable() error { return protocolError() }

func transportError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	var safe *safeError
	if errors.As(err, &safe) {
		return safe
	}
	var upstream *provider.HTTPError
	if errors.As(err, &upstream) {
		return responseError(ctx, upstream.StatusCode, nil)
	}
	// Never forward transport messages: they can contain signed URLs or cookies.
	return protocolError()
}

var (
	loginPattern  = regexp.MustCompile(`session.{0,20}expir|login.{0,20}expir|auth.{0,20}expir|登录已过期|会话已过期|cookie.{0,20}expir`)
	ratePattern   = regexp.MustCompile(`rate[ _-]?limit|too many requests|频率限制|请求过于频繁`)
	verifyPattern = regexp.MustCompile(`captcha|verification|verify|human.?check|人机验证|验证码`)
)

func decodeObject(data []byte) map[string]any {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if decoder.Decode(&value) != nil {
		return nil
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil
	}
	return value
}

func number(value any) int64 {
	text := ""
	switch value := value.(type) {
	case json.Number:
		text = string(value)
	case string:
		text = value
	}
	n, _ := strconv.ParseInt(text, 10, 64)
	return n
}

func isFailure(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != "" && value != "0"
	case json.Number:
		return value != "0"
	default:
		return true
	}
}

func objectError(obj map[string]any) bool {
	return isFailure(obj["error"]) || isFailure(obj["error_code"]) || isFailure(obj["code"]) || number(obj["status"]) >= 400 || number(obj["status_code"]) >= 400
}

func payloadError(data []byte) bool { return objectError(decodeObject(data)) }

func responseError(ctx context.Context, status int, data []byte) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	fields := decodeObject(data)
	var text strings.Builder
	verification, login, rate, rejected := false, false, false, false
	var visit func(map[string]any, int)
	visit = func(obj map[string]any, depth int) {
		if depth > 4 {
			return
		}
		for key, value := range obj {
			switch key {
			case "code", "error_code":
				n := number(value)
				rate = rate || n == 429
				rejected = rejected || n == 710022004
				login = login || n == 401 || n == 710012001
				verification = verification || n == 710022002
			case "status", "status_code":
				if n := number(value); n == 429 || (n == 401 && status != 429) {
					status = int(n)
				}
			case "semantic_reasoning":
				verification = verification || isFailure(value)
			case "type", "subtype", "error_type", "error_subtype", "message", "msg", "error_msg", "error", "body", "extra", "data":
				if s, ok := value.(string); ok {
					text.WriteString(strings.ToLower(s))
					text.WriteByte(' ')
					verification = verification || (key == "type" && s == "verify") || (key == "subtype" && s == "semantic_reasoning")
					if nested := decodeObject([]byte(s)); nested != nil {
						visit(nested, depth+1)
					}
				}
			}
			if child, ok := value.(map[string]any); ok {
				visit(child, depth+1)
			}
		}
	}
	visit(fields, 0)
	switch {
	case status == 429 || rate:
		return rateLimited()
	case verification:
		return verificationRequired()
	case status == 401 || login || loginPattern.MatchString(text.String()):
		return LoginRequired()
	case rejected && verifyPattern.MatchString(text.String()):
		return verificationRequired()
	case rejected:
		// The website also emits this code as "rate limited" before a captcha.
		// Keep recovery available without claiming every rejection is a challenge.
		return webRejected()
	case ratePattern.MatchString(text.String()):
		return rateLimited()
	case verifyPattern.MatchString(text.String()):
		return verificationRequired()
	default:
		return protocolError()
	}
}
