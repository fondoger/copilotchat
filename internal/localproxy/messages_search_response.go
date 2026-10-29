package localproxy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
)

// messagesSearchContext records the search contract actually sent upstream.
// Native search calls do not carry the client-facing tool name.
type messagesSearchContext struct{ mode, name string }

const messagesSearchReplayPrefix = "copilotchat-tool-search-v1:"

func validateMessagesSearch(item object, context *messagesSearchContext) error {
	if context == nil || context.name == "" || (context.mode != "client" && context.mode != "server") {
		return errors.New("Responses native tool search has no matching request context")
	}
	if text(item["execution"]) != context.mode {
		return errors.New("Responses native tool search execution differs from request")
	}
	if item["namespace"] != nil {
		return errors.New("Responses tool search namespaces cannot be converted")
	}
	if context.mode == "server" {
		if context.name != "tool_search_tool_regex" && context.name != "tool_search_tool_bm25" {
			return errors.New("Responses hosted search has an invalid tool name")
		}
		id, ok := item["call_id"]
		if !ok || id != nil {
			return errors.New("Responses hosted tool search requires a null call_id")
		}
	} else if text(item["call_id"]) == "" {
		return errors.New("Responses client tool search is missing call_id")
	}
	return nil
}

func messagesClientSearch(item object, context *messagesSearchContext) (object, error) {
	if err := validateMessagesSearch(item, context); err != nil {
		return nil, err
	}
	if context.mode != "client" || text(item["type"]) != "tool_search_call" || obj(item["arguments"]) == nil {
		return nil, errors.New("Responses client search requires object arguments")
	}
	return object{"type": "tool_use", "id": item["call_id"], "name": context.name, "input": item["arguments"]}, nil
}

func messagesHostedSearch(call, output object, context *messagesSearchContext) ([]any, error) {
	visible, err := messagesHostedSearchVisible(call, output, context)
	if err != nil {
		return nil, err
	}
	return messagesReplayBlocks([]any{call, output}, visible)
}

func messagesHostedSearchVisible(call, output object, context *messagesSearchContext) ([]any, error) {
	if context == nil || (context.name != "tool_search_tool_regex" && context.name != "tool_search_tool_bm25") {
		return nil, errors.New("invalid hosted tool search name")
	}
	if err := validateMessagesSearch(call, context); err != nil {
		return nil, err
	}
	if err := validateMessagesSearch(output, context); err != nil {
		return nil, err
	}
	if context.mode != "server" || call["type"] != "tool_search_call" || output["type"] != "tool_search_output" {
		return nil, errors.New("Responses hosted tool search requires a call/output pair")
	}
	for _, item := range []object{call, output} {
		if item["status"] != "completed" {
			return nil, errors.New("Responses hosted tool search is not completed")
		}
		if id, ok := item["id"]; ok && text(id) == "" {
			return nil, errors.New("Responses hosted tool search has an invalid item ID")
		}
	}
	if call["id"] != nil && call["id"] == output["id"] {
		return nil, errors.New("Responses hosted search call and output have the same item ID")
	}
	args := obj(call["arguments"])
	if args == nil {
		return nil, errors.New("Responses hosted search requires object arguments")
	}
	query := text(args["query"])
	if query == "" {
		query = text(args["pattern"])
	}
	if query == "" {
		paths := array(args["paths"])
		if paths == nil {
			return nil, errors.New("Responses hosted search has no query or paths")
		}
		names := make([]string, len(paths))
		for i, path := range paths {
			name, ok := path.(string)
			if !ok || name == "" {
				return nil, errors.New("Responses hosted search contains an invalid path")
			}
			names[i] = name
		}
		// Paths are native tool selections, not a regex execution. This display
		// input is descriptive only; the replay envelope retains the actual paths.
		query = strings.Join(names, " ")
	}
	tools := array(output["tools"])
	if tools == nil {
		return nil, errors.New("Responses hosted search output has no tools array")
	}
	references := make([]any, 0, len(tools))
	for _, value := range tools {
		tool := obj(value)
		if tool["type"] != "function" || text(tool["name"]) == "" || tool["namespace"] != nil {
			return nil, errors.New("Responses hosted search supports only plain function tools")
		}
		references = append(references, object{"type": "tool_reference", "tool_name": tool["name"]})
	}
	native := []any{call, output}
	data, err := json.Marshal(native)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256(data)
	id := fmt.Sprintf("srvtoolu_%x", hash[:12])
	field := "query"
	if context.name == "tool_search_tool_regex" {
		field = "pattern"
	}
	visible := []any{
		object{"type": "server_tool_use", "id": id, "name": context.name, "input": object{field: query}},
		object{"type": "tool_search_tool_result", "tool_use_id": id, "content": object{"type": "tool_search_tool_search_result", "tool_references": references}},
	}
	return visible, nil
}

func messagesNamespacedTool(item object) ([]any, error) {
	visible, err := messagesNamespacedToolVisible(item)
	if err != nil {
		return nil, err
	}
	return messagesReplayBlocks([]any{item}, visible)
}

func messagesNamespacedToolVisible(item object) ([]any, error) {
	if item["type"] != "function_call" || text(item["namespace"]) == "" {
		return nil, errors.New("invalid namespaced Responses function call")
	}
	if status, ok := item["status"]; ok && status != "completed" {
		return nil, errors.New("namespaced Responses function call is not completed")
	}
	block, err := responseTool(item)
	if err != nil {
		return nil, err
	}
	return []any{block}, nil
}

func messagesReplayBlocks(native, visible []any) ([]any, error) {
	envelope, err := json.Marshal(object{"native": native, "visible": visible})
	if err != nil {
		return nil, err
	}
	marker := object{"type": "thinking", "thinking": "", "signature": messagesSearchReplayPrefix + base64.RawStdEncoding.EncodeToString(envelope)}
	return append([]any{marker}, visible...), nil
}

// restoreMessagesSearchReplay validates this opaque (not authenticated or
// encrypted) bridge envelope against both native data and adjacent visible
// blocks. It leaves roles intact and gives the request converter native items.
func restoreMessagesSearchReplay(src object) error {
	messages := array(src["messages"])
	replacements := make([][]any, len(messages))
	for m, value := range messages {
		message := obj(value)
		blocks := array(message["content"])
		if blocks == nil {
			continue
		}
		var restored []any
		for i := 0; i < len(blocks); i++ {
			block := obj(blocks[i])
			if block["type"] == "responses_search_replay" {
				return errors.New("raw tool search replay blocks are internal only")
			}
			signature := text(block["signature"])
			if !strings.HasPrefix(signature, messagesSearchReplayPrefix) {
				if restored != nil {
					restored = append(restored, blocks[i])
				}
				continue
			}
			if message["role"] != "assistant" || block["type"] != "thinking" || text(block["thinking"]) != "" {
				return errors.New("invalid tool search replay marker")
			}
			data, err := base64.RawStdEncoding.DecodeString(strings.TrimPrefix(signature, messagesSearchReplayPrefix))
			if err != nil {
				return errors.New("invalid tool search replay encoding")
			}
			envelope, err := decodeObject(data)
			if err != nil {
				return errors.New("invalid tool search replay envelope")
			}
			native, visible := array(envelope["native"]), array(envelope["visible"])
			if len(visible) == 0 || i+len(visible) >= len(blocks) {
				return errors.New("incomplete tool search replay")
			}
			var mapped []any
			switch {
			case len(native) == 1 && len(visible) == 1:
				mapped, err = messagesNamespacedToolVisible(obj(native[0]))
			case len(native) == 2 && len(visible) == 2:
				name := text(obj(visible[0])["name"])
				mapped, err = messagesHostedSearchVisible(obj(native[0]), obj(native[1]), &messagesSearchContext{mode: "server", name: name})
			default:
				return errors.New("invalid tool search replay items")
			}
			if err != nil {
				return err
			}
			if !reflect.DeepEqual(mapped, visible) || !reflect.DeepEqual(visible, blocks[i+1:i+1+len(visible)]) {
				return errors.New("tool search replay visible blocks differ from native items")
			}
			if restored == nil {
				restored = make([]any, 0, len(blocks))
				restored = append(restored, blocks[:i]...)
			}
			restored = append(restored, object{"type": "responses_search_replay", "items": native})
			i += len(visible)
		}
		replacements[m] = restored
	}
	// Do not partially mutate a request that later fails validation.
	for i, blocks := range replacements {
		if blocks != nil {
			obj(messages[i])["content"] = blocks
		}
	}
	return nil
}

// nativeResponseState reconciles added/done/terminal items by output index.
// Hosted pairs are held until both complete, so their replay marker is emitted
// before the visible blocks, without setting the client tool-use stop reason.
type nativeResponseState struct {
	context        *messagesSearchContext
	pending        object
	pendingIndex   int
	completedItems map[int]object
}

// Copilot can rewrap item IDs between events. Keep the first completed item
// intact for replay, so later requests retain the same history prefix.
func sameResponseItem(a, b object) bool {
	a, b = cloneToolObject(a), cloneToolObject(b)
	delete(a, "id")
	delete(b, "id")
	return reflect.DeepEqual(a, b)
}

func (s *nativeResponseState) completedItem(index int, item object) (bool, error) {
	if old := s.completedItems[index]; old != nil {
		if !sameResponseItem(old, item) {
			return false, errors.New("Responses terminal item differs from streamed item")
		}
		return true, nil
	}
	if s.completedItems == nil {
		s.completedItems = map[int]object{}
	}
	return false, nil
}

func (s *nativeResponseState) item(index int, item object, done bool) ([]any, error) {
	if err := validateMessagesSearch(item, s.context); err != nil {
		return nil, err
	}
	if !done {
		return nil, nil
	}
	if completed, err := s.completedItem(index, item); completed || err != nil {
		return nil, err
	}
	if s.context.mode == "client" {
		block, err := messagesClientSearch(item, s.context)
		if err != nil {
			return nil, err
		}
		s.completedItems[index] = item
		return []any{block}, nil
	}
	switch text(item["type"]) {
	case "tool_search_call":
		if s.pending != nil {
			if index == s.pendingIndex && sameResponseItem(s.pending, item) {
				return nil, nil
			}
			return nil, errors.New("concurrent hosted tool search calls cannot be converted")
		}
		if obj(item["arguments"]) == nil {
			return nil, errors.New("Responses hosted search requires object arguments")
		}
		s.pending, s.pendingIndex = item, index
	case "tool_search_output":
		if s.pending == nil || index != s.pendingIndex+1 {
			return nil, errors.New("unpaired hosted tool search output")
		}
		blocks, err := messagesHostedSearch(s.pending, item, s.context)
		if err != nil {
			return nil, err
		}
		s.completedItems[s.pendingIndex], s.completedItems[index] = s.pending, item
		s.pending = nil
		return blocks, nil
	}
	return nil, nil
}

func (s *messagesStream) searchItem(index int, item object, done bool) error {
	blocks, err := s.search.item(index, item, done)
	if err != nil {
		return err
	}
	return s.searchBlocks(index, blocks)
}

func (s *messagesStream) namespacedTool(index int, item object, done bool) error {
	if !done {
		return nil
	}
	if completed, err := s.search.completedItem(index, item); completed || err != nil {
		return err
	}
	if s.byKey[fmt.Sprintf("tool:%d", index)] != nil {
		return errors.New("Responses function namespace arrived after streaming its tool block")
	}
	blocks, err := messagesNamespacedTool(item)
	if err != nil {
		return err
	}
	s.search.completedItems[index] = item
	return s.searchBlocks(index, blocks)
}

func (s *messagesStream) searchBlocks(index int, blocks []any) error {
	for i, value := range blocks {
		block := obj(value)
		kind := text(block["type"])
		initial := block
		if kind == "thinking" {
			initial = object{"type": "thinking", "thinking": ""}
		}
		if kind == "tool_use" {
			initial = object{"type": "tool_use", "id": block["id"], "name": block["name"], "input": object{}}
		}
		b, err := s.block(fmt.Sprintf("search:%d:%d", index, i), kind, initial)
		if err != nil {
			return err
		}
		if kind == "thinking" {
			if err := s.send("content_block_delta", object{"index": b.index, "delta": object{"type": "signature_delta", "signature": block["signature"]}}); err != nil {
				return err
			}
		}
		if kind == "tool_use" {
			s.tools = true
			data, err := json.Marshal(block["input"])
			if err != nil {
				return err
			}
			if err := s.fill(b, string(data)); err != nil {
				return err
			}
		}
		if err := s.closeBlock(b); err != nil {
			return err
		}
	}
	return nil
}
