package doubao

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/sse"
)

// Deliver one byte at a time, including inside UTF-8 and CRLF boundaries.
type byteReader struct{ io.Reader }

func (r byteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.Reader.Read(p)
}

func TestPrivateSSEFixtures(t *testing.T) {
	for _, tc := range []struct{ name, fixture, text, reasoning string }{
		{
			"distinct-idless-text-blocks",
			`data: {"content_block":[{"block_type":10000,"content":{"text_block":{"text":"ha"}}},{"block_type":10000,"content":{"text_block":{"text":"ha"}}}]}` + "\n\n",
			"haha", "",
		},
		{
			"same-thinking-block-finish",
			`event: STREAM_MSG_NOTIFY` + "\n" + `data: {"content":{"content_block":[{"block_type":10040,"block_id":"think","is_finish":false}]}}` + "\n\n" +
				`event: CHUNK_DELTA` + "\n" + `data: {"text":"steps"}` + "\n\n" +
				`event: STREAM_CHUNK` + "\n" + `data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10040,"block_id":"think","is_finish":true}]}}]}` + "\n\n" +
				`event: STREAM_CHUNK` + "\n" + `data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"block_id":"answer","is_finish":false,"content":{"text_block":{"text":"323"}}}]}}]}` + "\n\n",
			"323", "steps",
		},
		{
			"empty-compact-before-structured",
			`event: CHUNK_DELTA` + "\n" + `data: {"text":""}` + "\n\n" +
				`data: {"content_block":[{"block_type":10000,"content":{"text_block":{"text":"answer"}}}]}` + "\n\n",
			"answer", "",
		},
		{
			"search-block-and-legacy-text",
			`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10025}],"content":"{\"text\":\"answer\"}"}}]}` + "\n\n",
			"answer", "",
		},
		{
			"finished-marker-and-legacy-text",
			`data: {"content_block":[{"block_type":10040,"block_id":"think","is_finish":false}]}` + "\n\n" +
				`event: CHUNK_DELTA` + "\n" + `data: {"text":"steps"}` + "\n\n" +
				`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10040,"block_id":"think","is_finish":true}],"content":"{\"text\":\"answer\"}"}}]}` + "\n\n",
			"answer", "steps",
		},
		{
			"compact-thinking-and-snapshots",
			string([]byte{0xef, 0xbb, 0xbf}) + ": keepalive\r\n\r\nevent: SSE_ACK\r\ndata: {\"ack_client_meta\":{\"conversation_id\":\"remote\"}}\r\n\r\n" +
				"event: STREAM_CHUNK\ndata: {\"content\":{\"content_block\":[{\"block_type\":10040,\"block_id\":\"think-start\"}]}}\n\n" +
				"event: CHUNK_DELTA\ndata: {\"text\":\"思考\"}\n\n" +
				"event: STREAM_MSG_NOTIFY\ndata: {\"content\":{\"content_block\":[{\"block_type\":10040,\"block_id\":\"think-start\"},{\"block_type\":10000,\"content\":{\"text_block\":{\"text\":\"思考\"}}}]}}\n\n" +
				"data: {\"patch_op\":[{\"patch_value\":{\"content_block\":[{\"block_type\":10040,\"block_id\":\"think-end\"}]}}]}\n\n" +
				"event: CHUNK_DELTA\ndata: {\"text\":\"你好\"}\n\n" +
				"event: STREAM_MSG_NOTIFY\ndata: {\"content\":{\"content_block\":[{\"block_type\":10000,\"content\":{\"text_block\":{\"text\":\"你好\"}}},{\"block_type\":10025,\"content\":{\"search_query_result_block\":{\"summary\":\"not assistant text\"}}}]}}\n\n" +
				"event: CHUNK_DELTA\ndata: {\"text\":\"哈\"}\n\nevent: CHUNK_DELTA\ndata: {\"text\":\"哈\"}\n\n" +
				"data: {\"_event\":\"STREAM_MSG_NOTIFY\",\"content\":{\"content_block\":[{\"block_type\":10000,\"content\":{\"text_block\":{\"text\":\"你好哈哈\"}}}]}}\n\n",
			"你好哈哈", "思考",
		},
		{
			"ordered-blocks-and-legacy-patch",
			`data: {"content_block":[{"block_type":10040,"block_id":"start"},{"block_type":10000,"block_id":"r","content":{"text_block":{"text":"reason"}}},{"block_type":10040,"block_id":"end"},{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello"}}}]}` + "\n\n" +
				`data: {"patch_op":[{"patch_value":{"content_block":[{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello world"}}}]}}]}` + "\n\n" +
				`data: {"content_block":[{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello"}}}]}` + "\n\n" +
				`event: STREAM_MSG_NOTIFY` + "\n" + `data: {"content_block":[{"block_type":10040,"block_id":"start"},{"block_type":10000,"block_id":"r","content":{"text_block":{"text":"reason"}}},{"block_type":10040,"block_id":"end"},{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello world"}}}]}` + "\n\n" +
				`data: {"patch_op":[{"patch_value":{"content":"{\"text\":\"!\"}"}}]}` + "\n\n",
			"Hello world!", "reason",
		},
		{
			"multiline-and-final-event-without-blank-line",
			"event: STREAM_CHUNK\ndata: {\"patch_op\":[\ndata: {\"patch_value\":{\"content\":\"{\\\"text\\\":\\\"回答\\\"}\"}}]}",
			"回答", "",
		},
		{
			"compact-priority-in-same-event",
			`event: CHUNK_DELTA` + "\n" + `data: {"text":"once","content_block":[{"block_type":10000,"content":{"text_block":{"text":"once"}}}]}` + "\n\n" + `data: [DONE]` + "\n\n",
			"once", "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &testBody{Reader: byteReader{strings.NewReader(tc.fixture)}}
			session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream; charset=utf-8"}}, Body: body}, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			var text, reasoning strings.Builder
			completion, err := client.StreamWithEvents(context.Background(), baseRequest(), func(e provider.StreamEvent) {
				if e.Kind == provider.EventText {
					text.WriteString(e.Delta)
				} else if e.Kind == provider.EventReasoning {
					reasoning.WriteString(e.Delta)
				}
			})
			if err != nil || completion.Content != tc.text || completion.Reasoning != tc.reasoning || text.String() != tc.text || reasoning.String() != tc.reasoning {
				t.Fatalf("completion = %+v, events=(%q,%q), err=%v", completion, text.String(), reasoning.String(), err)
			}
			if completion.StopReason != "stop" {
				t.Fatal("no standard completion marker")
			}
			awaitClose(t, session)
		})
	}
}

func TestEmptyMalformedAndLateErrorStreams(t *testing.T) {
	for _, tc := range []struct {
		fixture string
		want    error
		partial string
	}{
		{"", protocolError(), ""},
		{"event: SSE_ACK\ndata: {}\n\n", protocolError(), ""},
		{"data: [DONE]\n\n", protocolError(), ""},
		{`data: {"content_block":[{"block_type":10040,"block_id":"think"}]}` + "\n\nevent: CHUNK_DELTA\ndata: {\"text\":\"only reasoning\"}\n\n", protocolError(), ""},
		{`event: STREAM_MSG_NOTIFY` + "\n" + `data: {"content_block":[{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello world"}}}]}` + "\n\nevent: STREAM_MSG_NOTIFY\n" + `data: {"content_block":[{"block_type":10000,"block_id":"a","content":{"text_block":{"text":"Hello, world"}}}]}` + "\n\n", protocolError(), "Hello world"},
		{"data: {broken}\n\n", protocolError(), ""},
		{`data: {"patch_op":[{"patch_value":{"content":"{broken}"}}]}` + "\n\n", protocolError(), ""},
		{completionSSE() + "event: gateway-error\ndata: {\"code\":710012001,\"message\":\"登录已过期 secret\"}\n\n", LoginRequired(), "answer"},
		{completionSSE() + "event: STREAM_ERROR\ndata: {\"error_code\":710022004,\"error_msg\":\"rate limited secret\"}\n\n", webRejected(), "answer"},
		{completionSSE() + "data: {\"error\":{\"type\":\"verify\",\"message\":\"secret\"}}\n\n", verificationRequired(), "answer"},
	} {
		session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
			resp, _ := response(200, "text/event-stream", tc.fixture)
			return resp, nil
		})
		client := NewClient(func(context.Context) (Session, error) { return session, nil })
		completion, err := client.StreamWithEvents(context.Background(), baseRequest(), nil)
		assertWebError(t, err, tc.want)
		if completion.Content != tc.partial {
			t.Fatalf("partial=%q want %q", completion.Content, tc.partial)
		}
		awaitClose(t, session)
	}
}

func TestConvertedStreamRetainsSafeMessage(t *testing.T) {
	var output bytes.Buffer
	convertSSE(context.Background(), strings.NewReader("event: STREAM_ERROR\ndata: {\"error_code\":429,\"error_msg\":\"secret\"}\n\n"), &output, "", nil)
	if err := sse.Read(&output, maxJSON, func(_ string, data string) error {
		var value struct {
			Error struct{ Message string }
		}
		if err := json.Unmarshal([]byte(data), &value); err != nil {
			return err
		}
		if value.Error.Message != "Doubao is rate limited; try again later" || strings.Contains(value.Error.Message, "secret") {
			t.Fatal("converted stream lost the safe error message")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
