package doubao

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/fondoger/copilotchat/internal/provider"
)

type conversationState struct {
	ID          string `json:"conversation_id"`
	HistoryHash string `json:"history_hash"`
	AnswerHash  string `json:"answer_hash"`
}

func validConversationID(id string) bool {
	return id != "" && len(id) <= 32 && strings.IndexFunc(id, func(r rune) bool { return r < '0' || r > '9' }) < 0
}

func resumeConversation(req provider.Request) conversationState {
	var state conversationState
	if req.Background || req.ConversationID == "" || len(req.Messages) < 3 {
		return state
	}
	n := len(req.Messages)
	previous := req.Messages[n-2]
	if previous.Role != "assistant" || req.Messages[n-1].Role != "user" ||
		json.Unmarshal(req.ConversationState, &state) != nil || !validConversationID(state.ID) {
		return conversationState{}
	}
	answer := sha256.Sum256([]byte(previous.Content))
	if state.HistoryHash == "" || state.HistoryHash != provider.HistoryHash(req.ConversationID, req.Messages[:n-2]) || state.AnswerHash != hex.EncodeToString(answer[:]) {
		return conversationState{}
	}
	return state
}
