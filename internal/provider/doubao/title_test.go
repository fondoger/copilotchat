package doubao

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

const titleConversationID = "38451321522173186"

func titleACK(numeric bool) string {
	id := `"` + titleConversationID + `"`
	if numeric {
		id = titleConversationID
	}
	return "event: SSE_ACK\ndata: {\"ack_client_meta\":{\"conversation_info\":{\"conversation_id\":" + id + ",\"name\":\"新对话\"}}}\n\n"
}

func titleResponse(id, name, extra string) string {
	data, _ := json.Marshal(map[string]any{"status_code": 0, "downlink_body": map[string]any{
		"get_conv_info_downlink_body": map[string]any{"conversation_info": map[string]any{
			"conversation_id": id, "name": name, "extra": extra,
		}},
	}})
	return string(data)
}

func TestConversationTitleUsesACKAndSameSessionAfterChatBodyCloses(t *testing.T) {
	for _, numeric := range []bool{false, true} {
		t.Run(map[bool]string{false: "string-id", true: "numeric-id"}[numeric], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var session *testSession
				var chatBody *testBody
				var titleBodies []*testBody
				queries := 0
				session = newTestSession(func(_ context.Context, path, contentType string, body []byte) (*http.Response, error) {
					if path == chatPath {
						resp, raw := response(200, "text/event-stream", titleACK(numeric)+completionSSE())
						chatBody = raw
						return resp, nil
					}
					if path != conversationInfoPath || contentType != "application/json; encoding=utf-8" || chatBody.closed.Load() != 1 || session.closed.Load() != 0 {
						t.Fatal("title query escaped the live session or overlapped the chat body")
					}
					payload := decodePayload(t, body)
					query := payload["uplink_body"].(map[string]any)["get_conv_info_uplink_body"].(map[string]any)
					if query["conversation_id"] != titleConversationID || number(payload["cmd"]) != 1110 {
						t.Fatal("ACK conversation identity lost precision or title request is invalid")
					}
					queries++
					name, extra := "新对话", `{"is_default_name":"true"}`
					if queries == 2 {
						name, extra = "Autumn leaves", `{"is_default_name":"false"}`
					}
					resp, raw := response(200, "application/json", titleResponse(titleConversationID, name, extra))
					titleBodies = append(titleBodies, raw)
					return resp, nil
				})
				client := NewClient(func(context.Context) (Session, error) { return session, nil })
				req := baseRequest()
				titles := make(chan string, 1)
				req.OnConversationTitle = func(title string) { titles <- title }
				result, err := client.StreamWithEvents(context.Background(), req, nil)
				if err != nil || result.Content != "answer" || queries != 2 {
					t.Fatalf("title lookup affected the answer: %q, queries=%d, err=%v", result.Content, queries, err)
				}
				select {
				case title := <-titles:
					if title != "Autumn leaves" {
						t.Fatalf("title = %q", title)
					}
				default:
					t.Fatal("successful inference returned before its title callback")
				}
				awaitClose(t, session)
				for _, body := range titleBodies {
					if body.closed.Load() != 1 {
						t.Fatal("title response body leaked or closed twice")
					}
				}
			})
		})
	}
}

func TestTitleLookupFailuresDoNotFailSuccessfulAnswers(t *testing.T) {
	for _, fixture := range []struct {
		name, body string
		status     int
	}{
		{"denied", `{"secret":"private upstream error"}`, 403},
		{"upstream-status", `{"status_code":712012002}`, 200},
		{"wrong-conversation", titleResponse("123", "Other title", `{"is_default_name":"false"}`), 200},
		{"missing-default-marker", titleResponse(titleConversationID, "Unconfirmed", `{}`), 200},
		{"malformed-extra", titleResponse(titleConversationID, "Unconfirmed", `invalid`), 200},
		{"oversized", strings.Repeat("x", maxJSON+1), 200},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			queries := 0
			session := newTestSession(func(_ context.Context, path, _ string, _ []byte) (*http.Response, error) {
				if path == chatPath {
					resp, _ := response(200, "text/event-stream", titleACK(false)+completionSSE())
					return resp, nil
				}
				queries++
				resp, _ := response(fixture.status, "application/json", fixture.body)
				return resp, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			req := baseRequest()
			req.OnConversationTitle = func(string) { t.Error("untrusted title reached the caller") }
			result, err := client.StreamWithEvents(context.Background(), req, nil)
			if err != nil || result.Content != "answer" || queries != 1 {
				t.Fatalf("answer=%q queries=%d err=%v", result.Content, queries, err)
			}
			awaitClose(t, session)
		})
	}
}

func TestTitleLookupIsOptInAndRequiresSuccessfulChatAndACK(t *testing.T) {
	for _, scenario := range []string{"disabled", "missing-ack", "empty-chat", "failed-chat", "background"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := titleACK(false) + completionSSE()
			if scenario == "missing-ack" {
				fixture = completionSSE()
			}
			if scenario == "empty-chat" {
				fixture = titleACK(false)
			}
			if scenario == "failed-chat" {
				fixture += "event: STREAM_ERROR\ndata: {\"error_code\":429}\n\n"
			}
			session := newTestSession(func(_ context.Context, path, _ string, _ []byte) (*http.Response, error) {
				if path != chatPath {
					t.Fatal("unnecessary title query")
				}
				resp, _ := response(200, "text/event-stream", fixture)
				return resp, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			req := baseRequest()
			if scenario != "disabled" && scenario != "background" {
				req.OnConversationTitle = func(string) { t.Error("unexpected title") }
			}
			req.Background = scenario == "background"
			_, err := client.StreamWithEvents(context.Background(), req, nil)
			if (err != nil) != (scenario == "empty-chat" || scenario == "failed-chat") {
				t.Fatalf("unexpected chat outcome: %v", err)
			}
			awaitClose(t, session)
		})
	}
}

func TestTitleLookupTimeoutAndCancellationReleaseSession(t *testing.T) {
	for _, scenario := range []string{"default-name", "blocked-request", "blocked-body", "canceled"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				var titleBody *testBody
				session := newTestSession(func(queryCtx context.Context, path, _ string, _ []byte) (*http.Response, error) {
					if path == chatPath {
						resp, _ := response(200, "text/event-stream", titleACK(false)+completionSSE())
						return resp, nil
					}
					switch scenario {
					case "default-name":
						resp, _ := response(200, "application/json", titleResponse(titleConversationID, "新对话", `{"is_default_name":true}`))
						return resp, nil
					case "blocked-body":
						r, w := io.Pipe()
						t.Cleanup(func() { w.Close() })
						titleBody = &testBody{Reader: r, onClose: func() { r.Close() }}
						return &http.Response{StatusCode: 200, Body: titleBody}, nil
					case "canceled":
						cancel()
					}
					<-queryCtx.Done()
					return nil, queryCtx.Err()
				})
				client := NewClient(func(context.Context) (Session, error) { return session, nil })
				req := baseRequest()
				req.OnConversationTitle = func(string) { t.Error("unfinished title published") }
				started := time.Now()
				result, err := client.StreamWithEvents(ctx, req, nil)
				if scenario == "canceled" {
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancellation lost: %v", err)
					}
				} else if err != nil || result.Content != "answer" || time.Since(started) != 4*time.Second {
					t.Fatalf("title timeout changed the answer or escaped its budget: %q, %v, %v", result.Content, err, time.Since(started))
				}
				awaitClose(t, session)
				if titleBody != nil && titleBody.closed.Load() != 1 {
					t.Fatal("timed-out title body leaked or closed twice")
				}
			})
		})
	}
}
