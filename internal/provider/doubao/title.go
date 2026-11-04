package doubao

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
)

const conversationInfoPath = "/im/conversation/info"

func ackConversationID(obj map[string]any) string {
	meta, _ := obj["ack_client_meta"].(map[string]any)
	info, _ := meta["conversation_info"].(map[string]any)
	for _, value := range []any{meta["conversation_id"], info["conversation_id"]} {
		var id string
		switch value := value.(type) {
		case string:
			id = value
		case json.Number:
			id = value.String()
		}
		if validConversationID(id) {
			return id
		}
	}
	return ""
}

// Titles arrive through IM metadata, independently of the completed chat stream.
// A missing or failed title must not turn a successful answer into an error.
func queryConversationTitle(ctx context.Context, life *sessionLife, conversationID string) string {
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		title, err := readConversationTitle(ctx, life, conversationID)
		if err != nil || title != "" {
			return title
		}
		select {
		case <-ctx.Done():
			return ""
		case <-ticker.C:
		}
	}
}

func readConversationTitle(ctx context.Context, life *sessionLife, conversationID string) (string, error) {
	body, err := json.Marshal(map[string]any{
		"cmd": 1110, "channel": 2, "version": "1", "sequence_id": provider.RequestID(),
		"uplink_body": map[string]any{"get_conv_info_uplink_body": map[string]any{
			"conversation_id": conversationID, "conversation_type": 3, "bot_id": "",
			"ext": map[string]string{"cold_start": "false"}, "option": map[string]bool{"need_bot_info": true},
		}},
	})
	if err != nil {
		return "", protocolError()
	}
	resp, err := life.do(ctx, conversationInfoPath, "application/json; encoding=utf-8", body)
	if err != nil {
		return "", err
	}
	stop := context.AfterFunc(ctx, life.closeBody)
	defer stop()
	defer life.closeBody()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxJSON+1))
	if err != nil || resp.StatusCode < http.StatusOK || resp.StatusCode >= 300 || len(data) > maxJSON {
		return "", protocolError()
	}
	var result struct {
		StatusCode json.RawMessage `json:"status_code"`
		Downlink   struct {
			Body struct {
				Info struct {
					ID    string `json:"conversation_id"`
					Name  string `json:"name"`
					Extra string `json:"extra"`
				} `json:"conversation_info"`
			} `json:"get_conv_info_downlink_body"`
		} `json:"downlink_body"`
	}
	if json.Unmarshal(data, &result) != nil || (string(result.StatusCode) != "0" && string(result.StatusCode) != `"0"`) {
		return "", protocolError()
	}
	info := result.Downlink.Body.Info
	var extra struct {
		DefaultName json.RawMessage `json:"is_default_name"`
	}
	if info.ID != conversationID || json.Unmarshal([]byte(info.Extra), &extra) != nil {
		return "", protocolError()
	}
	switch string(extra.DefaultName) {
	case "true", `"true"`:
		return "", nil
	case "false", `"false"`:
	default:
		return "", protocolError()
	}
	title := strings.TrimSpace(info.Name)
	if title == "" || title == "新对话" || strings.EqualFold(title, "New Chat") {
		return "", nil
	}
	return title, nil
}
