package webchat

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/png"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

const testConversation = "00000000-0000-4000-8000-000000000001"

func testImage(t *testing.T) string {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

type testSession struct {
	info  Info
	start func(context.Context, Input) (*http.Response, error)
	title func(context.Context, string) (string, error)
	close func()
	once  sync.Once
}

func (s *testSession) Info(context.Context) (Info, error) { return s.info, nil }
func (s *testSession) Start(ctx context.Context, in Input) (*http.Response, error) {
	return s.start(ctx, in)
}
func (s *testSession) Title(ctx context.Context, id string) (string, error) {
	if s.title != nil {
		return s.title(ctx, id)
	}
	return "", nil
}
func (s *testSession) Close() {
	s.once.Do(func() {
		if s.close != nil {
			s.close()
		}
	})
}

func fixture(t *testing.T, kind string) string {
	t.Helper()
	name := "chatgpt"
	if kind == Claude {
		name = "claude"
	}
	data, err := os.ReadFile("testdata/" + name + ".sse")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func testInfo(kind, scope string) Info {
	return Info{Account: provider.AccountInfo{ID: scope, Login: "web", Host: Host(kind)}, Models: []provider.Model{{ID: "observed-model", Vision: true, ThinkingEfforts: []string{"medium"}}}}
}

func streamResponse(body string) *http.Response {
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}, "X-Web-Conversation": {testConversation}}, Body: io.NopCloser(strings.NewReader(body))}
}

func TestRecordedPrivateStreamsExposeOnlyVisibleAnswer(t *testing.T) {
	for _, kind := range []string{ChatGPT, Claude} {
		t.Run(kind, func(t *testing.T) {
			var inputs []Input
			client := NewClient(kind, func(context.Context) (Session, error) {
				return &testSession{info: testInfo(kind, "account"), start: func(_ context.Context, in Input) (*http.Response, error) {
					inputs = append(inputs, in)
					return streamResponse(fixture(t, kind)), nil
				}}, nil
			})
			var visible strings.Builder
			result, err := client.StreamWithEvents(context.Background(), provider.Request{Model: "observed-model", ConversationID: "local", Messages: []provider.Message{{Role: "user", Content: "synthetic"}}}, func(e provider.StreamEvent) {
				if e.Kind == provider.EventText {
					visible.WriteString(e.Delta)
				}
			})
			want := "云杉-2915"
			if kind == Claude {
				want = "蓝色"
			}
			if err != nil || result.Content != want || visible.String() != want || result.Reasoning != "" || result.Usage.Available {
				t.Fatalf("answer=%q reasoning=%q err=%v", result.Content, result.Reasoning, err)
			}
			var state conversationState
			if json.Unmarshal(result.ConversationState, &state) != nil || !validID(state.ID) || !validID(state.Parent) || state.Scope != "account" {
				t.Fatal("successful stream lost its scoped continuation")
			}
			if len(inputs) != 1 || inputs[0].Prompt != "synthetic" {
				t.Fatal("first prompt was rewritten or repeated")
			}
		})
	}
}

func TestContinuationSurvivesClientRecreationAndUploadsOnlyNewImage(t *testing.T) {
	for _, kind := range []string{ChatGPT, Claude} {
		t.Run(kind, func(t *testing.T) {
			var inputs []Input
			open := func(context.Context) (Session, error) {
				return &testSession{info: testInfo(kind, "account"), start: func(_ context.Context, in Input) (*http.Response, error) {
					inputs = append(inputs, in)
					return streamResponse(fixture(t, kind)), nil
				}}, nil
			}
			first := provider.Request{Model: "observed-model", ConversationID: "local", Messages: []provider.Message{{Role: "user", Content: "first", Attachments: []provider.Attachment{{Name: "old.png", MIME: "image/png", Data: testImage(t)}}}}}
			result, err := NewClient(kind, open).StreamWithEvents(context.Background(), first, nil)
			if err != nil {
				t.Fatal(err)
			}
			follow := first
			follow.ConversationState = result.ConversationState
			follow.Messages = append(append([]provider.Message{}, first.Messages...), result.AssistantMessage(), provider.Message{Role: "user", Content: "follow", Attachments: []provider.Attachment{{Name: "new.png", MIME: "image/png", Data: testImage(t)}}})
			if _, err = NewClient(kind, open).StreamWithEvents(context.Background(), follow, nil); err != nil {
				t.Fatal(err)
			}
			if len(inputs) != 2 || inputs[1].ConversationID == "" || inputs[1].ParentID == "" || inputs[1].Prompt != "follow" || len(inputs[1].Images) != 1 || inputs[1].Images[0].Name != "new.png" {
				t.Fatal("follow-up did not use only the new message and image")
			}
			for _, change := range []string{"answer", "prompt", "local", "model", "background"} {
				t.Run(change, func(t *testing.T) {
					req := follow
					req.Messages = append([]provider.Message{}, follow.Messages...)
					switch change {
					case "answer":
						req.Messages[1].Content += " edited"
					case "prompt":
						req.Messages[0].Content += " edited"
					case "local":
						req.ConversationID = "copied"
					case "model":
						req.Model = "other"
					case "background":
						req.Background = true
					}
					state := resume(kind, req)
					if state.ID != "" {
						t.Fatal("changed local context reused the remote continuation")
					}
					in, err := prepare(req, state)
					if err != nil || in.ConversationID != "" || len(in.Images) != 2 {
						t.Fatal("fresh context lost history images")
					}
					var history []map[string]any
					if json.Unmarshal([]byte(in.Prompt), &history) != nil || len(history) != 3 {
						t.Fatal("fresh history lost role boundaries")
					}
				})
			}
		})
	}
}

func TestAccountChangeCannotSendOldHistory(t *testing.T) {
	kind := Claude
	calls := 0
	open := func(scope string) func(context.Context) (Session, error) {
		return func(context.Context) (Session, error) {
			return &testSession{info: testInfo(kind, scope), start: func(context.Context, Input) (*http.Response, error) {
				calls++
				return streamResponse(fixture(t, kind)), nil
			}}, nil
		}
	}
	req := provider.Request{Model: "observed-model", ConversationID: "local", Messages: []provider.Message{{Role: "user", Content: "private context"}}}
	result, err := NewClient(kind, open("first-account")).StreamWithEvents(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ConversationState = result.ConversationState
	req.Messages = append(req.Messages, result.AssistantMessage(), provider.Message{Role: "user", Content: "follow"})
	_, err = NewClient(kind, open("other-account")).StreamWithEvents(context.Background(), req, nil)
	if err == nil || calls != 1 {
		t.Fatal("history crossed a web account boundary")
	}
	req.AccountID = "first-account"
	req.ConversationState = nil // Editing or an earlier failure consumes the continuation, not the account binding.
	client := NewClient(kind, open("other-account"))
	if _, err := client.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.StreamWithEvents(context.Background(), req, nil); err == nil || calls != 1 {
		t.Fatal("reloading models let an old conversation cross accounts")
	}
}

func TestFailedStreamsNeverCommitContinuationOrUpstreamSecrets(t *testing.T) {
	for _, kind := range []string{ChatGPT, Claude} {
		for _, scenario := range []string{"truncated", "late-error", "http-denied"} {
			t.Run(kind+"/"+scenario, func(t *testing.T) {
				body := fixture(t, kind)
				end := "data: [DONE]"
				if kind == Claude {
					end = "event: message_stop"
				}
				pos := strings.LastIndex(body, end)
				if pos < 0 {
					t.Fatal("fixture has no completion")
				}
				if scenario == "truncated" {
					body = body[:pos]
				}
				if scenario == "late-error" {
					body = body[:pos] + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"message\":\"private signed token\"}}\n\n" + body[pos:]
				}
				client := NewClient(kind, func(context.Context) (Session, error) {
					return &testSession{info: testInfo(kind, "account"), start: func(context.Context, Input) (*http.Response, error) {
						r := streamResponse(body)
						if scenario == "http-denied" {
							r.StatusCode = 403
						}
						return r, nil
					}}, nil
				})
				req := provider.Request{Model: "observed-model", ConversationID: "local", Messages: []provider.Message{{Role: "user", Content: "test"}}}
				r, err := client.StreamWithEvents(context.Background(), req, nil)
				if err == nil || len(r.ConversationState) > 0 || r.ConversationTitle != "" || strings.Contains(err.Error(), "private") {
					t.Fatal("failed response looked successful or leaked details")
				}
			})
		}
	}
}

func TestUnsupportedHistoryFailsBeforeOpeningWebView(t *testing.T) {
	c := NewClient(Claude, func(context.Context) (Session, error) { t.Fatal("invalid request opened a page"); return nil, nil })
	for _, req := range []provider.Request{
		{Messages: []provider.Message{{Role: "tool", Content: "result"}}},
		{Messages: []provider.Message{{Role: "user", Content: "ok"}}, Tools: []provider.Tool{{Name: "tool"}}},
		{Messages: []provider.Message{{Role: "user", Content: "ok"}}, ThinkingBudgetTokens: 1024},
		{Messages: []provider.Message{{Role: "assistant", Replay: &provider.Replay{}}, {Role: "user", Content: "follow"}}},
	} {
		if _, err := c.StreamWithEvents(context.Background(), req, nil); err == nil {
			t.Fatal("unsupported history accepted")
		}
	}
}

func TestCancellationReleasesBlockedStream(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader, writer := io.Pipe()
	defer writer.Close()
	started := make(chan struct{})
	closed := make(chan struct{})
	c := NewClient(Claude, func(context.Context) (Session, error) {
		return &testSession{info: testInfo(Claude, "account"), start: func(context.Context, Input) (*http.Response, error) {
			close(started)
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: reader}, nil
		}, close: func() { reader.Close(); close(closed) }}, nil
	})
	done := make(chan error, 1)
	go func() {
		_, err := c.StreamWithEvents(ctx, provider.Request{Model: "observed-model", Messages: []provider.Message{{Role: "user", Content: "test"}}}, nil)
		done <- err
	}()
	<-started
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	<-closed
}
