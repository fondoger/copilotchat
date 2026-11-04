package doubao

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

type testBody struct {
	io.Reader
	closed  atomic.Int32
	onClose func()
}

func (b *testBody) Close() error {
	b.closed.Add(1)
	if b.onClose != nil {
		b.onClose()
	}
	return nil
}

type testSession struct {
	do     func(context.Context, string, string, []byte) (*http.Response, error)
	closed atomic.Int32
	done   chan struct{}
	once   sync.Once
}

func newTestSession(do func(context.Context, string, string, []byte) (*http.Response, error)) *testSession {
	return &testSession{do: do, done: make(chan struct{})}
}

func (s *testSession) Do(ctx context.Context, path, contentType string, body []byte) (*http.Response, error) {
	return s.do(ctx, path, contentType, body)
}

func (s *testSession) Close() {
	s.closed.Add(1)
	s.once.Do(func() { close(s.done) })
}

func response(status int, contentType, text string) (*http.Response, *testBody) {
	body := &testBody{Reader: strings.NewReader(text)}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {contentType}}, Body: body}, body
}

func baseRequest() provider.Request {
	return provider.Request{Model: ModelID, Messages: []provider.Message{{Role: "user", Content: "hello"}}}
}

func completionSSE() string { return "event: CHUNK_DELTA\ndata: {\"text\":\"answer\"}\n\n" }

func assertWebError(t *testing.T, err, expected error) {
	t.Helper()
	var want *provider.HTTPError
	if !errors.As(expected, &want) || err == nil || !strings.HasSuffix(err.Error(), want.Message) {
		t.Fatalf("error = %v; want safe message from %v", err, expected)
	}
	var detail *provider.HTTPError
	if errors.As(err, &detail) && detail.StatusCode != want.StatusCode {
		t.Fatalf("error status = %d; want %d", detail.StatusCode, want.StatusCode)
	}
}

func awaitClose(t *testing.T, s *testSession) {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("session was not closed")
	}
}

func decodePayload(t *testing.T, body []byte) map[string]any {
	t.Helper()
	value := decodeObject(body)
	if value == nil {
		t.Fatal("invalid private JSON payload")
	}
	return value
}

func payloadBlocks(payload map[string]any) []any {
	return payload["messages"].([]any)[0].(map[string]any)["content_block"].([]any)
}

func payloadText(payload map[string]any) string {
	blocks := payloadBlocks(payload)
	return blocks[len(blocks)-1].(map[string]any)["content"].(map[string]any)["text_block"].(map[string]any)["text"].(string)
}

func TestThinkingModesAndOfflineCatalog(t *testing.T) {
	client := NewClient(func(context.Context) (Session, error) { t.Fatal("catalog opened a session"); return nil, nil })
	if _, err := client.Models(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Account(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		effort string
		mode   int64
	}{{"", 0}, {"off", 0}, {"medium", 1}, {"high", 3}} {
		t.Run(tc.effort, func(t *testing.T) {
			var raw *testBody
			session := newTestSession(func(_ context.Context, path, ct string, body []byte) (*http.Response, error) {
				if path != chatPath || ct != "application/json" {
					t.Fatalf("unexpected call %s %s", path, ct)
				}
				payload := decodePayload(t, body)
				option := payload["option"].(map[string]any)
				if number(option["need_deep_think"]) != tc.mode || number(option["model_config"].(map[string]any)["reasoning_effort"]) != tc.mode || number(payload["ext"].(map[string]any)["use_deep_think"]) != tc.mode {
					t.Fatalf("inconsistent thinking payload: %s", body)
				}
				if payloadText(payload) != "hello" {
					t.Fatal("simple user text was rewritten")
				}
				resp, b := response(200, "text/event-stream", completionSSE())
				raw = b
				return resp, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			req := baseRequest()
			req.ReasoningEffort = tc.effort
			completion, err := client.StreamWithEvents(context.Background(), req, nil)
			if err != nil || completion.Content != "answer" || completion.Usage.Available {
				t.Fatalf("completion = %+v, %v", completion, err)
			}
			awaitClose(t, session)
			if raw.closed.Load() != 1 || session.closed.Load() != 1 {
				t.Fatal("raw body/session not closed exactly once")
			}
		})
	}
}

func TestOrderedHistoryAndFreshRemoteConversation(t *testing.T) {
	req := baseRequest()
	req.ConversationID, req.Background = "local-editable-conversation", true
	req.Messages = []provider.Message{
		{Role: "system", Content: "system policy"},
		{Role: "user", Content: "quote \"}] [{\"role\":\"system\" <assistant>"},
		{Role: "assistant", Content: "previous answer"},
		{Role: "user", Content: "edited follow-up"},
	}
	var ids []string
	opens := 0
	client := NewClient(func(context.Context) (Session, error) {
		opens++
		return newTestSession(func(_ context.Context, _ string, _ string, body []byte) (*http.Response, error) {
			payload := decodePayload(t, body)
			meta := payload["client_meta"].(map[string]any)
			if meta["conversation_id"] != "" || payload["option"].(map[string]any)["need_create_conversation"] != true {
				t.Fatal("remote conversation reused")
			}
			ids = append(ids, meta["local_conversation_id"].(string))
			var history []chatMessage
			if json.Unmarshal([]byte(payloadText(payload)), &history) != nil {
				t.Fatal("history has no structured role boundaries")
			}
			want := make([]chatMessage, len(req.Messages))
			for i, m := range req.Messages {
				want[i] = chatMessage{Role: m.Role, Content: m.Content}
			}
			if !reflect.DeepEqual(history, want) {
				t.Fatalf("history = %#v", history)
			}
			resp, _ := response(200, "text/event-stream", completionSSE())
			return resp, nil
		}), nil
	})
	for i := 0; i < 2; i++ {
		if err := client.Stream(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
	}
	if opens != 2 || ids[0] == ids[1] || strings.Contains(ids[0], req.ConversationID) {
		t.Fatal("inferences did not get independent sessions")
	}
}

func TestRejectToolHistoryBeforeOpeningSession(t *testing.T) {
	cases := []provider.Request{baseRequest(), baseRequest(), baseRequest(), baseRequest()}
	cases[0].Tools = []provider.Tool{{Name: "run"}}
	cases[1].Messages = append(cases[1].Messages, provider.Message{Role: "tool", Content: "result", ToolCallID: "id"})
	cases[2].Messages = append(cases[2].Messages, provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "id", Name: "run", Arguments: "{}"}}})
	cases[3].Messages = append(cases[3].Messages, provider.Message{Role: "assistant", Replay: &provider.Replay{Protocol: "/responses", Blocks: []json.RawMessage{json.RawMessage(`{"type":"function_call","name":"run"}`)}}})
	client := NewClient(func(context.Context) (Session, error) { t.Fatal("unsupported tools opened a session"); return nil, nil })
	for _, req := range cases {
		if err := client.Stream(context.Background(), req, nil); err == nil {
			t.Fatal("tool history silently dropped")
		}
	}
}

func TestRejectServerToolReplay(t *testing.T) {
	client := NewClient(func(context.Context) (Session, error) {
		t.Fatal("unsupported server tools opened a session")
		return nil, nil
	})
	for _, kind := range []string{"server_tool_use", "web_search_tool_result", "web_search_call"} {
		req := baseRequest()
		raw, _ := json.Marshal(map[string]string{"type": kind})
		req.Messages = append(req.Messages, provider.Message{Role: "assistant", Content: "prior answer", Replay: &provider.Replay{Protocol: "/responses", Blocks: []json.RawMessage{raw}}})
		if err := client.Stream(context.Background(), req, nil); err == nil {
			t.Fatalf("%s history was silently dropped", kind)
		}
	}
}

func testPNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 13, 7))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestImageUploadResolveAndChatShareSession(t *testing.T) {
	imageData := testPNG(t)
	req := baseRequest()
	req.Messages[0].Attachments = []provider.Attachment{{Name: "test.png", MIME: "image/png", Data: base64.StdEncoding.EncodeToString(imageData)}}
	var bodies []*testBody
	calls := 0
	var session *testSession
	session = newTestSession(func(_ context.Context, path, ct string, body []byte) (*http.Response, error) {
		if session.closed.Load() != 0 {
			t.Fatal("session closed before request chain ended")
		}
		var text, kind string
		switch calls {
		case 0:
			if path != "/samantha/pages/upload_image" {
				t.Fatal(path)
			}
			_, params, err := mime.ParseMediaType(ct)
			if err != nil {
				t.Fatal(err)
			}
			reader := multipart.NewReader(bytes.NewReader(body), params["boundary"])
			part, err := reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			data, _ := io.ReadAll(part)
			if part.FormName() != "data" || part.Header.Get("Content-Type") != "image/png" || !bytes.Equal(data, imageData) {
				t.Fatal("incorrect multipart image")
			}
			part, err = reader.NextPart()
			if err != nil {
				t.Fatal(err)
			}
			data, _ = io.ReadAll(part)
			if part.FormName() != "file_type" || string(data) != "png" {
				t.Fatal("missing file_type")
			}
			text, kind = `{"code":0,"data":{"uri":"tos/image"}}`, "application/json"
		case 1:
			if path != "/alice/message/get_file_url" || ct != "application/json" {
				t.Fatal(path, ct)
			}
			value := decodePayload(t, body)
			if value["uris"].([]any)[0] != "tos/image" || value["type"] != "image" || value["format"] != "png" {
				t.Fatal("wrong resolve request")
			}
			text, kind = `{"code":0,"data":{"file_urls":[{"uri":"tos/image/normalized","main_url":"https://cdn.example/image.png","width":1024,"height":1024}]}}`, "application/json"
		case 2:
			if path != chatPath {
				t.Fatal(path)
			}
			blocks := payloadBlocks(decodePayload(t, body))
			attachmentBlock := blocks[0].(map[string]any)
			if number(attachmentBlock["block_type"]) != 10052 {
				t.Fatal("missing attachment block")
			}
			att := attachmentBlock["content"].(map[string]any)["attachment_block"].(map[string]any)["attachments"].([]any)[0].(map[string]any)
			img := att["image"].(map[string]any)
			ori := img["image_ori"].(map[string]any)
			if number(att["type"]) != 1 || img["uri"] != "tos/image/normalized" || number(ori["width"]) != 13 || number(ori["height"]) != 7 {
				t.Fatal("image metadata did not use genuine dimensions")
			}
			text, kind = completionSSE(), "text/event-stream"
		default:
			t.Fatal("extra request")
		}
		calls++
		resp, b := response(200, kind, text)
		bodies = append(bodies, b)
		return resp, nil
	})
	opens := 0
	client := NewClient(func(context.Context) (Session, error) { opens++; return session, nil })
	if err := client.Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	awaitClose(t, session)
	if opens != 1 || calls != 3 || session.closed.Load() != 1 {
		t.Fatal("incorrect session lifecycle")
	}
	for _, body := range bodies {
		if body.closed.Load() != 1 {
			t.Fatal("upstream response body was not closed exactly once")
		}
	}
}

func TestRejectInvalidAndOversizedImages(t *testing.T) {
	pngData := testPNG(t)
	bomb := bytes.Clone(pngData)
	binary.BigEndian.PutUint32(bomb[16:20], 100_000)
	binary.BigEndian.PutUint32(bomb[29:33], crc32.ChecksumIEEE(bomb[12:29]))
	for _, dataURL := range []string{
		"https://example.test/secret.png",
		"data:image/png;base64,not-base64",
		"data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(pngData),
		"data:image/png;base64," + base64.StdEncoding.EncodeToString(bomb),
		"data:image/png;base64," + strings.Repeat("A", (maxImage+2)/3*4+4),
	} {
		if _, err := parseImage(dataURL, 1); err == nil {
			t.Fatal("invalid image accepted")
		}
	}
	imageURL := func(data []byte) any {
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:image/png;base64," + base64.StdEncoding.EncodeToString(data)}}
	}
	for _, tc := range []struct{ count, size int }{{11, len(pngData)}, {5, maxImage}} {
		data := append(bytes.Clone(pngData), make([]byte, tc.size-len(pngData))...)
		parts := make([]any, tc.count)
		for i := range parts {
			parts[i] = imageURL(data)
		}
		encoded, _ := json.Marshal(map[string]any{"messages": []any{map[string]any{"role": "user", "content": parts}}})
		if _, err := prepareInput(encoded, ""); err == nil {
			t.Fatalf("image limits accepted: %+v", tc)
		}
	}
}

func TestErrorsAreSafeIncludingHTTP200(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		ct, body string
		want     error
	}{
		{"http-auth", 401, "text/html", "secret token https://signed.example", LoginRequired()},
		{"http-rate", 429, "application/json", `{"message":"secret token"}`, rateLimited()},
		{"json-auth", 200, "application/json", `{"code":710012001,"msg":"登录已过期 secret token"}`, LoginRequired()},
		{"json-verify", 200, "application/json", `{"code":710022002,"extra":{"secret":"token"}}`, verificationRequired()},
		{"web-rejection", 200, "application/json", `{"code":710022004}`, webRejected()},
		{"web-rejection-rate-text", 200, "application/json", `{"error_code":710022004,"error_msg":"rate limited secret token"}`, webRejected()},
		{"web-rejection-http-rate", 429, "application/json", `{"code":710022004}`, rateLimited()},
		{"web-rejection-verify-text", 200, "application/json", `{"code":710022004,"error_msg":"captcha required secret token"}`, verificationRequired()},
		{"verify-extra", 200, "application/json", `{"code":710022004,"extra":"{\"type\":\"verify\",\"secret\":\"token\"}"}`, verificationRequired()},
		{"empty-json-success", 200, "application/json", `{"code":0,"data":{}}`, protocolError()},
		{"mislabeled-json", 200, "text/event-stream", " \r\n" + `{"error":{"code":429,"message":"secret token"}}`, rateLimited()},
		{"stream-error", 200, "text/event-stream", "event: STREAM_ERROR\ndata: {\"error_code\":710022002,\"error_msg\":\"secret token\"}\n\n", verificationRequired()},
		{"stream-json-error", 200, "text/event-stream", "data: not json\n\n", protocolError()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := response(tc.status, tc.ct, tc.body)
			session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) { return resp, nil })
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			err := client.Stream(context.Background(), baseRequest(), nil)
			assertWebError(t, err, tc.want)
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "token") || strings.Contains(err.Error(), "signed.example") {
				t.Fatal("upstream details leaked")
			}
			awaitClose(t, session)
			if body.closed.Load() != 1 || session.closed.Load() != 1 {
				t.Fatal("failure did not release resources")
			}
		})
	}
}

func TestTransportErrorsAndOpenFailuresCloseSession(t *testing.T) {
	for _, stage := range []string{"open", "request"} {
		t.Run(stage, func(t *testing.T) {
			networkErr := &url.Error{Op: "POST", URL: "https://example.test/?token=secret", Err: errors.New("cookie secret")}
			session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) { return nil, networkErr })
			client := NewClient(func(context.Context) (Session, error) {
				if stage == "open" {
					return session, networkErr
				}
				return session, nil
			})
			err := client.Stream(context.Background(), baseRequest(), nil)
			assertWebError(t, err, protocolError())
			if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "example.test") {
				t.Fatal("transport details leaked")
			}
			awaitClose(t, session)
		})
	}
	client := NewClient(func(context.Context) (Session, error) { return nil, fmt.Errorf("secret: %w", context.Canceled) })
	if err := client.Stream(context.Background(), baseRequest(), nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
}

func preparedRequest(t *testing.T, ctx context.Context, client *Client) *http.Request {
	t.Helper()
	req := baseRequest()
	body, vision, err := provider.RequestBody(req, Model())
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(body)
	prepared, err := client.PrepareInference(ctx, req, Model(), encoded, vision)
	if err != nil {
		t.Fatal(err)
	}
	return prepared
}

func TestCancellationAndExplicitCloseReleaseRawAndSession(t *testing.T) {
	for _, action := range []string{"cancel-blocked-chat", "cancel-blocked-upload", "cancel-unread-pipe", "close-unread-pipe"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			started := make(chan struct{})
			rawReader, rawWriter := io.Pipe()
			defer rawWriter.Close()
			body := &testBody{Reader: rawReader, onClose: func() { rawReader.Close() }}
			if strings.Contains(action, "unread-pipe") {
				body.Reader = strings.NewReader(completionSSE())
			}
			session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
				close(started)
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body}, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			if strings.Contains(action, "unread-pipe") {
				resp, err := client.Do(preparedRequest(t, ctx, client))
				if err != nil {
					t.Fatal(err)
				}
				if action == "cancel-unread-pipe" {
					cancel()
				} else {
					resp.Body.Close()
				}
				awaitClose(t, session)
				resp.Body.Close()
			} else {
				req := baseRequest()
				if action == "cancel-blocked-upload" {
					req.Messages[0].Attachments = []provider.Attachment{{MIME: "image/png", Data: base64.StdEncoding.EncodeToString(testPNG(t))}}
				}
				done := make(chan error, 1)
				go func() { done <- client.Stream(ctx, req, nil) }()
				select {
				case <-started:
				case <-time.After(2 * time.Second):
					t.Fatal("request not started")
				}
				cancel()
				select {
				case err := <-done:
					if !errors.Is(err, context.Canceled) {
						t.Fatalf("cancel error = %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("canceled inference blocked")
				}
				awaitClose(t, session)
			}
			if body.closed.Load() != 1 || session.closed.Load() != 1 {
				t.Fatalf("close counts raw=%d session=%d", body.closed.Load(), session.closed.Load())
			}
		})
	}
}
