package localproxy

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

// Count only on the same provider and model that generation would use. Never
// substitute a generation request to obtain usage: counting must not generate
// output, execute tools or introduce an extra billable completion.
func writeCountTokens(w http.ResponseWriter, r *http.Request, ctx context.Context, client provider.RawClient, model provider.Model, raw provider.RawRequest) {
	src, err := decodeObject(raw.Body)
	if err != nil || len(array(src["messages"])) == 0 {
		apiError(w, 400, "messages must be a non-empty array")
		return
	}
	for _, value := range array(src["messages"]) {
		m := obj(value)
		switch text(m["role"]) {
		case "user", "assistant", "system", "developer":
		default:
			apiError(w, 400, "Unsupported message role")
			return
		}
		if _, ok := m["content"].(string); !ok && array(m["content"]) == nil {
			apiError(w, 400, "Message content must be a string or array")
			return
		}
	}
	if supports(model, "/v1/messages") {
		native := raw
		native.Endpoint = "/v1/messages/count_tokens"
		if r.URL.Query().Get("beta") == "true" {
			native.RawQuery = "beta=true"
		}
		forwardTokenCount(w, ctx, client, native, "anthropic")
		return
	}
	if supports(model, "/responses") {
		// Reuse the generation translation so system prompts, tool schemas,
		// tool results, images and reasoning history are counted as sent.
		body, _, err := messagesToResponses(raw.Body, model)
		if err != nil {
			apiError(w, 400, err.Error())
			return
		}
		fields, _ := decodeObject(body)
		count := object{}
		// Official input_tokens parameters, excluding generation-only options
		// such as stream, store, include, max_output_tokens and temperature.
		for _, key := range []string{"model", "input", "instructions", "tools", "tool_choice", "parallel_tool_calls", "reasoning", "text"} {
			if value, ok := fields[key]; ok {
				count[key] = value
			}
		}
		raw.Body, _ = json.Marshal(count)
		raw.Endpoint, raw.RawQuery = "/responses/input_tokens", ""
		raw.Header = raw.Header.Clone()
		raw.Header.Del("Anthropic-Version")
		raw.Header.Del("Anthropic-Beta")
		forwardTokenCount(w, ctx, client, raw, "responses")
		return
	}
	apiError(w, http.StatusNotFound, "The model does not offer a token-counting API.")
}

// Forward upstream errors unchanged; never substitute a locally estimated count.
func forwardTokenCount(w http.ResponseWriter, ctx context.Context, client provider.RawClient, raw provider.RawRequest, source string) {
	res, err := client.Forward(ctx, raw)
	if err != nil {
		apiError(w, 502, "Upstream token counting failed. Check the provider's sign-in and connection.")
		return
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 {
		apiError(w, 502, "Could not read upstream token count")
		return
	}
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		apiError(w, 502, "The provider returned an unsupported redirect.")
		return
	}
	for key, values := range res.Header {
		lower := strings.ToLower(key)
		if lower == "retry-after" || lower == "request-id" || lower == "x-request-id" || strings.HasPrefix(lower, "anthropic-ratelimit-") || strings.HasPrefix(lower, "x-ratelimit-") {
			w.Header()[key] = append([]string(nil), values...)
		}
	}
	if res.StatusCode >= 200 && res.StatusCode < 300 {
		var count struct {
			InputTokens *int64 `json:"input_tokens"`
		}
		if json.Unmarshal(body, &count) != nil || count.InputTokens == nil || *count.InputTokens < 0 {
			apiError(w, 502, "Provider returned an invalid token count")
			return
		}
		if source == "responses" {
			body, _ = json.Marshal(count) // Adapt OpenAI's response.input_tokens object to Anthropic.
		}
		w.Header().Set("X-CopilotChat-Token-Count-Source", source)
	}
	contentType := res.Header.Get("Content-Type")
	if source == "responses" && res.StatusCode >= 200 && res.StatusCode < 300 {
		contentType = "application/json"
	}
	if contentType != "" {
		w.Header().Set("Content-Type", contentType)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(body)
	return
}
