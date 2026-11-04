package doubao

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func continuationRequest(t *testing.T) provider.Request {
	t.Helper()
	session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
		resp, _ := response(200, "text/event-stream", titleACK(false)+completionSSE())
		return resp, nil
	})
	client := NewClient(func(context.Context) (Session, error) { return session, nil })
	req := baseRequest()
	req.ConversationID = "local-chat"
	result, err := client.StreamWithEvents(context.Background(), req, nil)
	if err != nil || len(result.ConversationState) == 0 {
		t.Fatalf("initial answer has no continuation: %v", err)
	}
	awaitClose(t, session)
	req.Messages = append(req.Messages, result.AssistantMessage(), provider.Message{Role: "user", Content: "follow up"})
	req.ConversationState = result.ConversationState
	return req
}

func TestFollowUpsReuseRemoteConversationAcrossClientsWithoutReplayingHistory(t *testing.T) {
	req := continuationRequest(t)
	for range 2 {
		session := newTestSession(func(_ context.Context, path, _ string, body []byte) (*http.Response, error) {
			payload := decodePayload(t, body)
			meta := payload["client_meta"].(map[string]any)
			if path != chatPath || meta["conversation_id"] != titleConversationID || meta["local_conversation_id"] != "" || payload["option"].(map[string]any)["need_create_conversation"] != false {
				t.Fatal("follow-up created a new remote conversation")
			}
			if payloadText(payload) != req.Messages[len(req.Messages)-1].Content || len(payloadBlocks(payload)) != 1 {
				t.Fatal("follow-up replayed earlier messages or attachments")
			}
			// Existing conversations may omit an ACK; retain the requested ID.
			resp, _ := response(200, "text/event-stream", "event: CHUNK_DELTA\ndata: {\"text\":\"ans\"}\n\nevent: CHUNK_DELTA\ndata: {\"text\":\"wer\"}\n\n")
			return resp, nil
		})
		client := NewClient(func(context.Context) (Session, error) { return session, nil })
		result, err := client.StreamWithEvents(context.Background(), req, nil)
		if err != nil || result.Content != "answer" || len(result.ConversationState) == 0 {
			t.Fatalf("follow-up lost its answer or continuation: %v", err)
		}
		awaitClose(t, session)
		req.Messages = append(req.Messages, result.AssistantMessage(), provider.Message{Role: "user", Content: "another question"})
		req.ConversationState = result.ConversationState
	}
}

func TestChangedHistoryOrInvalidStateStartsFreshWithFullHistory(t *testing.T) {
	for _, scenario := range []string{"prompt", "answer", "local-chat", "system", "missing-state", "invalid-state", "background"} {
		t.Run(scenario, func(t *testing.T) {
			req := continuationRequest(t)
			switch scenario {
			case "prompt":
				req.Messages[0].Content = "edited question"
			case "answer":
				req.Messages[1].Content = "edited answer"
			case "local-chat":
				req.ConversationID = "forked-chat"
			case "system":
				req.Messages = append([]provider.Message{{Role: "system", Content: "new policy"}}, req.Messages...)
			case "missing-state":
				req.ConversationState = nil
			case "invalid-state":
				req.ConversationState = json.RawMessage(`{"conversation_id":"not-an-id"}`)
			case "background":
				req.Background = true
			}
			session := newTestSession(func(_ context.Context, _ string, _ string, body []byte) (*http.Response, error) {
				payload := decodePayload(t, body)
				meta := payload["client_meta"].(map[string]any)
				if meta["conversation_id"] != "" || !strings.HasPrefix(meta["local_conversation_id"].(string), "local_") || payload["option"].(map[string]any)["need_create_conversation"] != true {
					t.Fatal("changed history was appended to the old remote context")
				}
				var history []chatMessage
				if json.Unmarshal([]byte(payloadText(payload)), &history) != nil || len(history) != len(req.Messages) {
					t.Fatal("fresh conversation lost local history")
				}
				for i, message := range req.Messages {
					if history[i].Role != message.Role || history[i].Content != message.Content {
						t.Fatal("fresh conversation rewrote local history")
					}
				}
				resp, _ := response(200, "text/event-stream", completionSSE())
				return resp, nil
			})
			client := NewClient(func(context.Context) (Session, error) { return session, nil })
			if err := client.Stream(context.Background(), req, nil); err != nil {
				t.Fatal(err)
			}
			awaitClose(t, session)
		})
	}
}

func TestFollowUpUploadsOnlyNewImage(t *testing.T) {
	req := baseRequest()
	req.ConversationID = "images"
	image := base64.StdEncoding.EncodeToString(testPNG(t))
	req.Messages[0].Attachments = []provider.Attachment{{Name: "first.png", MIME: "image/png", Data: image}}
	calls := map[string]int{}
	open := func(context.Context) (Session, error) {
		return newTestSession(func(_ context.Context, path, _ string, body []byte) (*http.Response, error) {
			calls[path]++
			var text, kind string
			switch path {
			case "/samantha/pages/upload_image":
				text, kind = `{"code":0,"data":{"uri":"tos/image"}}`, "application/json"
			case "/alice/message/get_file_url":
				text, kind = `{"code":0,"data":{"file_urls":[{"uri":"tos/image","main_url":"https://cdn.example/image.png"}]}}`, "application/json"
			case chatPath:
				if calls[chatPath] == 2 && len(payloadBlocks(decodePayload(t, body))) != 2 {
					t.Fatal("follow-up duplicated image attachments")
				}
				text, kind = titleACK(false)+completionSSE(), "text/event-stream"
			default:
				t.Fatalf("unexpected endpoint: %s", path)
			}
			resp, _ := response(200, kind, text)
			return resp, nil
		}), nil
	}
	first, err := NewClient(open).StreamWithEvents(context.Background(), req, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.ConversationState = first.ConversationState
	req.Messages = append(req.Messages, first.AssistantMessage(), provider.Message{Role: "user", Content: "compare", Attachments: []provider.Attachment{{Name: "next.png", MIME: "image/png", Data: image}}})
	if err := NewClient(open).Stream(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if calls["/samantha/pages/upload_image"] != 2 || calls["/alice/message/get_file_url"] != 2 || calls[chatPath] != 2 {
		t.Fatalf("historical images were uploaded again: %v", calls)
	}
}

func TestFollowUpRejectsAnotherRemoteConversationWithoutRetry(t *testing.T) {
	req := continuationRequest(t)
	calls := 0
	session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
		calls++
		fixture := strings.ReplaceAll(titleACK(false), titleConversationID, "123") + completionSSE()
		resp, _ := response(200, "text/event-stream", fixture)
		return resp, nil
	})
	client := NewClient(func(context.Context) (Session, error) { return session, nil })
	result, err := client.StreamWithEvents(context.Background(), req, nil)
	if err == nil || calls != 1 || len(result.ConversationState) != 0 {
		t.Fatalf("mismatched conversation was accepted or retried: calls=%d err=%v", calls, err)
	}
	awaitClose(t, session)
}

func TestFailedOrUnidentifiedAnswerHasNoContinuation(t *testing.T) {
	for _, fixture := range []string{
		completionSSE(),
		titleACK(false),
		titleACK(false) + completionSSE() + "event: STREAM_ERROR\ndata: {\"error_code\":429}\n\n",
	} {
		session := newTestSession(func(context.Context, string, string, []byte) (*http.Response, error) {
			resp, _ := response(200, "text/event-stream", fixture)
			return resp, nil
		})
		client := NewClient(func(context.Context) (Session, error) { return session, nil })
		result, _ := client.StreamWithEvents(context.Background(), baseRequest(), nil)
		if len(result.ConversationState) != 0 {
			t.Fatal("failed or unidentified answer published continuation metadata")
		}
		awaitClose(t, session)
	}
}
