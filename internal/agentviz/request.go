package agentviz

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// Payload limits apply only to the observer. The gateway forwards the original
// bytes unchanged. Binary inputs are represented by a digest, never base64 copies.
const maxDisplayBytes = 2 << 20

type object = map[string]any

func decode(data []byte, dst any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	return d.Decode(dst)
}
func obj(v any) object          { o, _ := v.(map[string]any); return o }
func arr(v any) []any           { a, _ := v.([]any); return a }
func str(v any) string          { s, _ := v.(string); return s }
func integer(v any) int64       { n, _ := v.(json.Number); i, _ := n.Int64(); return max(0, i) }
func raw(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func digest(data []byte) string { h := sha256.Sum256(data); return hex.EncodeToString(h[:]) }

func displayValue(v any) any {
	switch x := v.(type) {
	case map[string]any:
		out := make(object, len(x))
		for k, value := range x {
			if s, ok := value.(string); ok && ((k == "data" && str(x["type"]) == "base64") || strings.HasPrefix(s, "data:")) {
				out[k] = object{"omitted": "binary payload", "bytes": len(s), "sha256": digest([]byte(s))}
			} else {
				out[k] = displayValue(value)
			}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, v := range x {
			out[i] = displayValue(v)
		}
		return out
	case string:
		if len(x) > maxDisplayBytes {
			// The digest preserves identity even when the inspector clips a huge value.
			return object{"preview": strings.ToValidUTF8(x[:maxDisplayBytes], "�"), "omittedBytes": len(x) - maxDisplayBytes, "sha256": digest([]byte(x))}
		}
	}
	return v
}

type atom struct {
	role, kind string
	data       json.RawMessage
}
type parsedRequest struct {
	identity, client, model, previousID, parentIdentity string
	purpose                                             string
	forkAt                                              int
	explicitBranch                                      bool
	scheduled, compacted                                bool
	atoms                                               []atom
	system, tools                                       json.RawMessage
}

func makeAtom(role string, value any) atom {
	v := obj(value)
	if v != nil {
		copy := make(object, len(v))
		for k, x := range v {
			if k != "cache_control" {
				copy[k] = x
			}
		}
		value = copy
	}
	return atom{role: role, kind: str(v["type"]), data: raw(displayValue(value))}
}

// Canonicalize wire representation only: a text string and a text block carry
// the same history, as do tool JSON arguments with different whitespace/order.
func contentAtoms(role string, content any) []atom {
	if text, ok := content.(string); ok {
		content = []any{object{"type": "text", "text": text}}
	}
	var result []atom
	for _, v := range arr(content) {
		b := obj(v)
		if b == nil {
			continue
		}
		kind := str(b["type"])
		switch kind {
		case "input_text", "output_text":
			b = object{"type": "text", "text": b["text"]}
		case "text":
			b = object{"type": "text", "text": b["text"]}
		case "tool_use":
			b = object{"type": kind, "id": b["id"], "name": b["name"], "input": b["input"]}
		case "thinking":
			b = object{"type": kind, "thinking": b["thinking"], "signature": b["signature"]}
		}
		if role == "user" && str(b["type"]) == "text" {
			text, injected := stripReminders(str(b["text"]))
			for _, s := range injected {
				result = append(result, makeAtom("system", object{"type": "text", "text": s}))
			}
			if text == "" {
				continue
			}
			b = object{"type": "text", "text": text}
		}
		// Claude Code may concatenate consecutive text blocks when replaying.
		if len(result) > 0 && str(b["type"]) == "text" && result[len(result)-1].role == role && result[len(result)-1].kind == "text" {
			var prev object
			_ = decode(result[len(result)-1].data, &prev)
			b = object{"type": "text", "text": str(prev["text"]) + "\n" + str(b["text"])}
			result[len(result)-1] = makeAtom(role, b)
		} else {
			result = append(result, makeAtom(role, b))
		}
	}
	return result
}

func stripReminders(text string) (string, []string) {
	var reminders []string
	// Only recognize complete harness wrappers, never loose keyword matches.
	for {
		start := strings.Index(text, "<system-reminder>")
		if start < 0 {
			break
		}
		end := strings.Index(text[start:], "</system-reminder>")
		if end < 0 {
			break
		}
		end += start + len("</system-reminder>")
		reminders = append(reminders, text[start:end])
		text = text[:start] + text[end:]
	}
	if len(reminders) == 0 {
		return text, nil
	}
	return strings.TrimSpace(text), reminders
}

func requestIdentity(h http.Header, body object) (string, string) {
	client := "Agent"
	if strings.Contains(strings.ToLower(h.Get("User-Agent")), "claude") {
		client = "Claude Code"
	}
	if strings.Contains(strings.ToLower(h.Get("User-Agent")), "codex") {
		client = "Codex"
	}
	id := ""
	for _, key := range []string{"X-Claude-Code-Session-Id", "Thread-Id", "Session-Id", "Session_id", "X-ConversationId"} {
		if v := strings.TrimSpace(h.Get(key)); v != "" {
			id = v
			if key == "X-Claude-Code-Session-Id" {
				client = "Claude Code"
			}
			break
		}
	}
	metadata := obj(body["metadata"])
	if user := str(metadata["user_id"]); user != "" {
		var fields object
		if decode([]byte(user), &fields) == nil {
			if id == "" {
				id = str(fields["session_id"])
			}
		} else if i := strings.LastIndex(user, "_session_"); id == "" && i >= 0 {
			id = user[i+len("_session_"):]
		}
		if id != "" {
			client = "Claude Code"
		}
	}
	// Subagents share the parent session ID but have a distinct agent ID.
	for _, key := range []string{"X-Claude-Code-Agent-Id", "X-Agent-Id"} {
		if agent := h.Get(key); agent != "" {
			id += "/agent/" + agent
			break
		}
	}
	return id, client
}

func parseRequest(body []byte, h http.Header, protocol string) (*parsedRequest, error) {
	var envelope object
	if err := decode(body, &envelope); err != nil {
		return nil, err
	}
	p := &parsedRequest{model: str(envelope["model"]), previousID: str(envelope["previous_response_id"]), forkAt: -1}
	p.identity, p.client = requestIdentity(h, envelope)
	p.explicitBranch = h.Get("X-Claude-Code-Agent-Id") != "" || h.Get("X-Agent-Id") != "" || h.Get("X-Claude-Code-Parent-Agent-Id") != "" || bytes.Contains(raw(envelope["system"]), []byte("cc_is_subagent=true"))
	if agent := h.Get("X-Claude-Code-Agent-Id"); agent != "" {
		p.parentIdentity = strings.TrimSuffix(p.identity, "/agent/"+agent)
		if parent := h.Get("X-Claude-Code-Parent-Agent-Id"); parent != "" {
			p.parentIdentity += "/agent/" + parent
		}
	}
	p.scheduled = strings.Contains(h.Get("User-Agent"), "workload/cron") || bytes.Contains(raw(envelope["system"]), []byte("cc_workload=cron"))
	p.system = raw(displayValue(envelope["system"]))
	p.tools = raw(displayValue(envelope["tools"]))
	if protocol == "/responses" {
		p.system = raw(displayValue(envelope["instructions"]))
		if s, ok := envelope["input"].(string); ok {
			p.atoms = contentAtoms("user", s)
		}
		for _, value := range arr(envelope["input"]) {
			p.atoms = append(p.atoms, responseItemAtoms(obj(value))...)
		}
	} else {
		var systems []any
		for _, value := range arr(envelope["messages"]) {
			m := obj(value)
			role := str(m["role"])
			if role == "system" || role == "developer" {
				systems = append(systems, m["content"])
				p.atoms = append(p.atoms, contentAtoms("system", m["content"])...)
				continue
			}
			if role == "tool" {
				p.atoms = append(p.atoms, makeAtom("user", object{"type": "tool_result", "tool_use_id": m["tool_call_id"], "content": m["content"]}))
				continue
			}
			p.atoms = append(p.atoms, contentAtoms(role, m["content"])...)
			for _, value := range arr(m["tool_calls"]) {
				call := obj(value)
				fn := obj(call["function"])
				var input any
				if decode([]byte(str(fn["arguments"])), &input) != nil {
					input = fn["arguments"]
				}
				p.atoms = append(p.atoms, makeAtom("assistant", object{"type": "tool_use", "id": call["id"], "name": fn["name"], "input": input}))
			}
		}
		if envelope["system"] == nil && len(systems) > 0 {
			p.system = raw(displayValue(systems))
		}
	}
	for _, a := range p.atoms {
		if a.role == "user" && strings.HasPrefix(atomText(a), "This session is being continued from a previous conversation") {
			p.compacted = true
		}
	}
	p.classifyPurpose()
	if len(p.atoms) == 0 && p.previousID == "" {
		return nil, fmt.Errorf("no observable message history")
	}
	return p, nil
}

const suggestionInstruction = "[SUGGESTION MODE: Suggest what the user might naturally type next into Claude Code.]"

// A helper reuses the main session ID, tools and system prompt. Its current
// instruction is the purpose signal; old quoted markers and tool output are not.
// This also recognizes tool continuations within a suggestion's private branch.
func (p *parsedRequest) classifyPurpose() {
	if p.client != "Claude Code" || p.identity == "" {
		return
	}
	for i := len(p.atoms) - 1; i >= 0; i-- {
		a := p.atoms[i]
		if a.role != "user" || a.kind == "tool_result" || synthetic(a) {
			continue
		}
		if a.kind == "text" && strings.HasPrefix(strings.TrimSpace(atomText(a)), suggestionInstruction) {
			p.purpose, p.forkAt = "suggestion", i
		}
		return
	}
}

func responseItemAtoms(item object) []atom {
	switch str(item["type"]) {
	case "function_call", "custom_tool_call":
		var input any
		if str(item["type"]) == "custom_tool_call" {
			input = item["input"]
		} else if decode([]byte(str(item["arguments"])), &input) != nil {
			input = item["arguments"]
		}
		return []atom{makeAtom("assistant", object{"type": "tool_use", "id": item["call_id"], "name": item["name"], "input": input})}
	case "function_call_output", "custom_tool_call_output":
		return []atom{makeAtom("user", object{"type": "tool_result", "tool_use_id": item["call_id"], "content": item["output"]})}
	case "reasoning":
		return []atom{makeAtom("assistant", item)}
	default:
		role := str(item["role"])
		if role == "" {
			role = "assistant"
		}
		return contentAtoms(role, item["content"])
	}
}

func atomText(a atom) string { var o object; _ = decode(a.data, &o); return str(o["text"]) }
func synthetic(a atom) bool {
	if a.role == "system" || a.role == "developer" {
		return true
	}
	if a.role != "user" || a.kind != "text" {
		return false
	}
	t := strings.TrimSpace(atomText(a))
	for _, prefix := range []string{"<task-notification>", "<local-command-caveat>", "<command-message>", "<tool-result>", "<system_notification>", "<system-notification>", "[Request interrupted by user", "Stop hook feedback:", "Output token limit hit. Resume directly", "This session is being continued from a previous conversation"} {
		if strings.HasPrefix(t, prefix) {
			return true
		}
	}
	return false
}

// If capture starts mid-session, only the newest input belongs to the observed
// turn. Earlier user messages still enter the shared ancestry tree.
func currentInstruction(atoms []atom) []atom {
	last := -1
	for i, a := range atoms {
		if a.role == "user" && a.kind != "tool_result" && !synthetic(a) {
			last = i
		}
	}
	if last < 0 {
		return atoms
	}
	start := last
	for start > 0 && atoms[start-1].role == "user" && atoms[start-1].kind != "tool_result" {
		start--
	}
	return atoms[start : last+1]
}

func triggerFor(atoms []atom) string {
	for _, a := range atoms {
		if a.role == "user" && a.kind != "tool_result" && !synthetic(a) {
			return "user"
		}
	}
	return "continuation"
}
