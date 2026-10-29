package localproxy

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/fondoger/copilotchat/internal/provider"
)

func TestResponsesCompressedReasoningReplay(t *testing.T) {
	random := rand.New(rand.NewPCG(1, 2))
	opaque := make([]byte, 12<<10)
	for i := range opaque {
		opaque[i] = byte(random.Uint32())
	}
	reply := parsed(t, responsesReply)
	reasoning := obj(array(reply["output"])[0])
	reasoning["encrypted_content"] = base64.StdEncoding.EncodeToString(opaque)
	reasoning["future"] = object{"number": json.Number("9007199254740993"), "nested": []any{"保留 <opaque>", true, nil}}
	original, _ := json.Marshal(reasoning)
	legacy := reasoningSignaturePrefix + base64.RawStdEncoding.EncodeToString(original)
	encodedReply, _ := json.Marshal(reply)

	for _, stream := range []bool{false, true} {
		t.Run(map[bool]string{false: "JSON", true: "SSE"}[stream], func(t *testing.T) {
			upstream := response(string(encodedReply))
			upstream.Header.Set("Content-Type", "application/json")
			if stream {
				upstream = response(sse(
					`{"type":"response.output_item.done","output_index":0,"item":`+string(original)+`}`,
					`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"Hel"}`,
					`{"type":"response.output_text.delta","output_index":1,"content_index":0,"delta":"lo"}`,
					`{"type":"response.completed","response":`+string(encodedReply)+`}`,
				))
			}
			w := httptest.NewRecorder()
			writeMessagesResponse(w, upstream, "public", stream, nil)
			var content []any
			if !stream {
				content = array(parsed(t, w.Body.String())["content"])
			} else {
				arguments := map[int]string{}
				if err := readResponseEvents(strings.NewReader(w.Body.String()), func(event object) error {
					index := number(event["index"])
					switch text(event["type"]) {
					case "content_block_start":
						content = append(content, event["content_block"])
					case "content_block_delta":
						delta, block := obj(event["delta"]), obj(content[index])
						switch text(delta["type"]) {
						case "thinking_delta":
							block["thinking"] = text(block["thinking"]) + text(delta["thinking"])
						case "text_delta":
							block["text"] = text(block["text"]) + text(delta["text"])
						case "signature_delta":
							block["signature"] = text(block["signature"]) + text(delta["signature"])
						case "input_json_delta":
							arguments[index] += text(delta["partial_json"])
						}
					case "error":
						t.Fatal(event)
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
				for index, value := range arguments {
					obj(content[index])["input"] = parsed(t, value)
				}
			}
			if len(content) != 3 || text(obj(content[1])["text"]) != "Hello" {
				t.Fatal("reply content changed", content)
			}
			thinking := obj(content[0])
			signature := text(thinking["signature"])
			if len(signature) >= len(legacy)*4/5 {
				t.Fatalf("opaque reasoning signature was not reduced: %d bytes, previously %d", len(signature), len(legacy))
			}
			for _, replay := range []string{signature, legacy} {
				thinking["signature"] = replay
				body, _ := json.Marshal(object{"messages": []any{
					object{"role": "assistant", "content": content},
					object{"role": "user", "content": []any{object{"type": "tool_result", "tool_use_id": "call_1", "content": "done"}}},
				}})
				converted, _, err := messagesToResponses(body, provider.Model{ID: "gpt-fixture"})
				if err != nil {
					t.Fatal(err)
				}
				input := array(parsed(t, string(converted))["input"])
				if len(input) != 4 || !reflect.DeepEqual(obj(input[0]), reasoning) || text(obj(input[2])["arguments"]) != `{"number":9007199254740993}` || obj(input[3])["output"] != "done" {
					t.Fatal("reasoning or tool continuation changed")
				}
			}
		})
	}
}

func TestGatewayRejectsInvalidCompressedReasoning(t *testing.T) {
	compress := func(data []byte) string {
		var compressed bytes.Buffer
		writer := zlib.NewWriter(&compressed)
		_, _ = writer.Write(data)
		_ = writer.Close()
		return reasoningSignaturePrefix + "z1:" + base64.RawStdEncoding.EncodeToString(compressed.Bytes())
	}
	valid := compress([]byte(`{"type":"reasoning","encrypted_content":"opaque"}`))
	data, _ := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(valid, reasoningSignaturePrefix+"z1:"))
	corrupt := bytes.Clone(data)
	corrupt[len(corrupt)-1] ^= 1
	called := make(chan struct{}, 10)
	_, base := fixture(t, &rawClient{fn: func(context.Context, provider.RawRequest) (*http.Response, error) {
		called <- struct{}{}
		return response(responsesReply), nil
	}})
	for _, test := range []struct{ name, signature string }{
		{"bad base64", reasoningSignaturePrefix + "z1:!"},
		{"bad header", reasoningSignaturePrefix + "z1:" + base64.RawStdEncoding.EncodeToString([]byte("invalid"))},
		{"truncated", reasoningSignaturePrefix + "z1:" + base64.RawStdEncoding.EncodeToString(data[:len(data)-1])},
		{"checksum", reasoningSignaturePrefix + "z1:" + base64.RawStdEncoding.EncodeToString(corrupt)},
		{"unknown version", strings.Replace(valid, ":z1:", ":z2:", 1)},
		{"wrong item", compress([]byte(`{"type":"message"}`))},
		{"extra JSON", compress([]byte(`{"type":"reasoning"}{"type":"reasoning"}`))},
		{"expansion limit", compress([]byte(`{"type":"reasoning","encrypted_content":"` + strings.Repeat("x", MaxBody) + `"}`))},
	} {
		t.Run(test.name, func(t *testing.T) {
			body, _ := json.Marshal(object{"model": "gpt-fixture", "messages": []any{
				object{"role": "assistant", "content": []any{object{"type": "thinking", "thinking": "", "signature": test.signature}}},
				object{"role": "user", "content": "next"},
			}})
			res := send(t, "POST", base+"/v1/messages", string(body), map[string]string{"X-Api-Key": "fixture-key"})
			defer res.Body.Close()
			if res.StatusCode != http.StatusBadRequest {
				data, _ := io.ReadAll(res.Body)
				t.Fatal(res.Status, string(data))
			}
		})
	}
	if len(called) != 0 {
		t.Fatal("invalid reasoning reached the provider")
	}
}
