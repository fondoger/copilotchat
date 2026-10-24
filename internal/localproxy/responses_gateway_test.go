package localproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestResponsesSearchThroughGatewayAndFollowUp(t *testing.T) {
	reply := `{"id":"msg_search","type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"thinking","thinking":"Search first","signature":"original-signature"},{"type":"server_tool_use","id":"srv1","name":"web_search","input":{"query":"example"}},{"type":"web_search_tool_result","tool_use_id":"srv1","content":[{"type":"web_search_result","url":"https://example.com","title":"Example","encrypted_content":"original-ciphertext"}]},{"type":"text","text":"Found it","citations":[{"type":"web_search_result_location","url":"https://example.com","title":"Example","encrypted_index":"original-index","cited_text":"Found it"}]}],"usage":{"input_tokens":4,"output_tokens":2}}`
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			calls := make(chan provider.RawRequest, 2)
			_, base := fixture(t, &rawClient{fn: func(_ context.Context, r provider.RawRequest) (*http.Response, error) {
				calls <- r
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(reply))}, nil
			}})
			input := []any{object{"role": "user", "content": "search"}}
			body := object{"model": "claude-fixture", "input": input, "stream": stream, "tools": []any{object{"type": "web_search"}}, "instructions": "stable"}
			headers := map[string]string{"Authorization": "Bearer fixture-key", "Thread-Id": "search-thread"}
			exchange := func() object {
				t.Helper()
				encoded, _ := json.Marshal(body)
				res := send(t, "POST", base+"/responses", string(encoded), headers)
				data, _ := io.ReadAll(res.Body)
				if res.StatusCode != 200 {
					t.Fatal(res.Status, string(data))
				}
				if !stream {
					return parsed(t, string(data))
				}
				var terminal object
				if err := readResponseEvents(strings.NewReader(string(data)), func(e object) error {
					if e["type"] == "response.completed" {
						terminal = obj(e["response"])
					}
					if e["type"] == "response.failed" || e["type"] == "error" {
						t.Fatal(e)
					}
					return nil
				}); err != nil || terminal == nil {
					t.Fatal(err, string(data))
				}
				return terminal
			}
			first := exchange()
			sent := <-calls
			firstBody := parsed(t, string(sent.Body))
			expected := parsed(t, reply)["content"]
			output := array(first["output"])
			if len(output) < 3 || obj(output[0])["type"] != "web_search_call" || obj(output[len(output)-1])["type"] != "reasoning" {
				t.Fatal(first)
			}
			body["input"] = append(append(input, output...), object{"role": "user", "content": "Explain that source without searching"})
			exchange()
			next := <-calls
			nextBody := parsed(t, string(next.Body))
			history := array(nextBody["messages"])
			actual, _ := json.Marshal(obj(history[1])["content"])
			original, _ := json.Marshal(expected)
			if string(actual) != string(original) || firstBody["system"] != nextBody["system"] || sent.ConversationID != next.ConversationID || next.AgentInitiated {
				t.Fatal("gateway lost search history or turn metadata", string(next.Body))
			}
			if obj(array(firstBody["tools"])[0])["type"] != "web_search_20250305" || sent.Endpoint != "/v1/messages" {
				t.Fatal("search was not routed natively", sent)
			}
		})
	}
}

func TestResponsesBridgeCancelsUpstreamStream(t *testing.T) {
	started, cancelled := make(chan struct{}), make(chan struct{})
	_, base := fixture(t, &rawClient{fn: func(ctx context.Context, r provider.RawRequest) (*http.Response, error) {
		reader, writer := io.Pipe()
		go func() {
			defer writer.Close()
			io.WriteString(writer, sse(`{"type":"message_start","message":{"id":"msg_cancel","type":"message","role":"assistant","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}`))
			close(started)
			<-ctx.Done()
			close(cancelled)
		}()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
	}})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, "POST", base+"/responses", strings.NewReader(`{"model":"claude-fixture","input":"hi","stream":true}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer fixture-key")
	done := make(chan error, 1)
	go func() {
		res, err := http.DefaultClient.Do(request)
		if err == nil {
			_, err = io.Copy(io.Discard, res.Body)
			res.Body.Close()
		}
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream did not start")
	}
	cancel()
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("upstream context was not cancelled")
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("client did not exit")
	}
}
