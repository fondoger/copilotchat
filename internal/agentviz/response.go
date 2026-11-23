package agentviz

import (
	"bytes"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

const maxEventBytes = 8 << 20

type responseParser struct {
	protocol          string
	buffer            []byte
	data              []byte
	discardLine       bool
	stream            bool
	terminal          bool
	stop, id, problem string
	usage             Usage
	blocks            map[int]object
	arguments         map[int]string
	output            []atom
	chatText          strings.Builder
	chatTools         map[int]object
	clipped           bool
}

func newResponseParser(protocol string) *responseParser {
	return &responseParser{protocol: protocol, blocks: map[int]object{}, arguments: map[int]string{}, chatTools: map[int]object{}}
}

func (p *responseParser) feed(chunk []byte, stream bool) {
	p.stream = stream
	if !stream {
		if len(p.buffer)+len(chunk) <= maxEventBytes {
			p.buffer = append(p.buffer, chunk...)
		} else {
			p.clipped = true
		}
		return
	}
	// Incremental line splitting preserves multi-line data and CRLF frames even
	// when transport writes split a UTF-8 code point or an SSE delimiter.
	for len(chunk) > 0 {
		i := bytes.IndexByte(chunk, '\n')
		n := len(chunk)
		if i >= 0 {
			n = i + 1
		}
		if !p.discardLine {
			if len(p.buffer)+n > maxEventBytes {
				p.buffer = nil
				p.discardLine = true
				p.clipped = true
			} else {
				p.buffer = append(p.buffer, chunk[:n]...)
			}
		}
		chunk = chunk[n:]
		if i < 0 {
			break
		}
		if !p.discardLine {
			p.line(bytes.TrimRight(p.buffer, "\r\n"))
		}
		p.buffer = nil
		p.discardLine = false
	}
}
func (p *responseParser) line(line []byte) {
	if len(line) == 0 {
		if len(p.data) > 0 {
			p.event(bytes.TrimSuffix(p.data, []byte("\n")))
			p.data = nil
		}
		return
	}
	if bytes.HasPrefix(line, []byte("data:")) {
		value := bytes.TrimPrefix(line[5:], []byte(" "))
		if len(p.data)+len(value) > maxEventBytes {
			p.clipped = true
			return
		}
		p.data = append(p.data, value...)
		p.data = append(p.data, '\n')
	}
}
func (p *responseParser) event(data []byte) {
	if bytes.Equal(data, []byte("[DONE]")) {
		if p.protocol == "/chat/completions" && p.stop != "" {
			p.terminal = true
		}
		return
	}
	var e object
	if decode(data, &e) != nil {
		return
	}
	if errorObject := obj(e["error"]); errorObject != nil {
		p.problem = str(errorObject["message"])
		if p.problem == "" {
			p.problem = "Upstream stream error"
		}
		return
	}
	if p.protocol == "/v1/messages" {
		p.messageEvent(e)
	} else if p.protocol == "/responses" {
		p.responseEvent(e)
	} else {
		p.chatEvent(e)
	}
}
func (p *responseParser) anthropicUsage(u object) {
	if u == nil {
		return
	}
	// message_delta usage is cumulative, not additive. Cache fields often only
	// occur in message_start; preserve them when the final delta omits them.
	if v, ok := u["cache_read_input_tokens"]; ok {
		p.usage.CachedTokens = integer(v)
	}
	if v, ok := u["input_tokens"]; ok {
		p.usage.InputTokens = integer(v) + p.usage.CachedTokens + integer(u["cache_creation_input_tokens"])
	}
	if v, ok := u["output_tokens"]; ok {
		p.usage.OutputTokens = integer(v)
	}
	p.usage.totals()
}
func (p *responseParser) openAIUsage(u object, chat bool) {
	if u == nil {
		return
	}
	input, output, details := "input_tokens", "output_tokens", "input_tokens_details"
	if chat {
		input, output, details = "prompt_tokens", "completion_tokens", "prompt_tokens_details"
	}
	p.usage.InputTokens = integer(u[input])
	p.usage.OutputTokens = integer(u[output])
	p.usage.CachedTokens = integer(obj(u[details])["cached_tokens"])
	p.usage.totals()
}
func (p *responseParser) messageEvent(e object) {
	idx := int(integer(e["index"]))
	switch str(e["type"]) {
	case "message_start":
		m := obj(e["message"])
		p.id = str(m["id"])
		p.anthropicUsage(obj(m["usage"]))
	case "content_block_start":
		p.blocks[idx] = obj(e["content_block"])
	case "content_block_delta":
		d := obj(e["delta"])
		b := p.blocks[idx]
		if b == nil {
			b = object{"type": "text", "text": ""}
			p.blocks[idx] = b
		}
		key := ""
		switch str(d["type"]) {
		case "text_delta":
			key = "text"
		case "thinking_delta":
			key = "thinking"
		case "signature_delta":
			key = "signature"
		case "input_json_delta":
			if len(p.arguments[idx]) < maxDisplayBytes {
				p.arguments[idx] += str(d["partial_json"])
			} else {
				p.clipped = true
			}
			return
		}
		if key != "" {
			if len(str(b[key])) < maxDisplayBytes {
				b[key] = str(b[key]) + str(d[key])
			} else {
				p.clipped = true
			}
		}
	case "content_block_stop":
		if text, ok := p.arguments[idx]; ok {
			var input any
			if decode([]byte(text), &input) == nil {
				p.blocks[idx]["input"] = input
			} else {
				p.blocks[idx]["input"] = text
			}
			delete(p.arguments, idx)
		}
	case "message_delta":
		p.stop = str(obj(e["delta"])["stop_reason"])
		p.anthropicUsage(obj(e["usage"]))
	case "message_stop":
		p.terminal = p.stop != ""
	case "error":
		p.problem = "Upstream stream error"
	}
}
func (p *responseParser) responseEvent(e object) {
	switch str(e["type"]) {
	case "response.completed", "response.incomplete", "response.failed":
		p.readResponse(obj(e["response"]))
	case "response.output_item.done":
		p.blocks[int(integer(e["output_index"]))] = obj(e["item"])
	case "response.created":
		p.id = str(obj(e["response"])["id"])
	case "error":
		p.problem = str(e["message"])
	}
}
func (p *responseParser) readResponse(r object) {
	p.id = str(r["id"])
	p.openAIUsage(obj(r["usage"]), false)
	p.output = nil
	for _, v := range arr(r["output"]) {
		p.output = append(p.output, responseItemAtoms(obj(v))...)
	}
	switch str(r["status"]) {
	case "completed":
		p.terminal = true
		p.stop = "end_turn"
		for _, a := range p.output {
			if a.kind == "tool_use" {
				p.stop = "tool_use"
			}
		}
	case "incomplete":
		p.terminal = true
		p.stop = "max_tokens"
		p.problem = "Response incomplete: " + str(obj(r["incomplete_details"])["reason"])
	case "failed", "cancelled":
		p.terminal = true
		p.problem = "Response " + str(r["status"])
	}
	if e := obj(r["error"]); e != nil {
		p.problem = str(e["message"])
	}
}
func (p *responseParser) chatEvent(e object) {
	p.id = str(e["id"])
	p.openAIUsage(obj(e["usage"]), true)
	for _, v := range arr(e["choices"]) {
		choice := obj(v)
		if integer(choice["index"]) != 0 {
			continue
		}
		if stop := str(choice["finish_reason"]); stop != "" {
			p.stop = stop
		}
		d := obj(choice["delta"])
		if len(p.chatText.String()) < maxDisplayBytes {
			p.chatText.WriteString(str(d["content"]))
		} else {
			p.clipped = true
		}
		for _, value := range arr(d["tool_calls"]) {
			call := obj(value)
			idx := int(integer(call["index"]))
			t := p.chatTools[idx]
			if t == nil {
				t = object{}
				p.chatTools[idx] = t
			}
			if id := str(call["id"]); id != "" {
				t["id"] = id
			}
			fn := obj(call["function"])
			if name := str(fn["name"]); name != "" {
				t["name"] = name
			}
			if len(str(t["arguments"])) < maxDisplayBytes {
				t["arguments"] = str(t["arguments"]) + str(fn["arguments"])
			} else {
				p.clipped = true
			}
		}
	}
}
func (p *responseParser) finish() {
	if p.stream {
		// An SSE event is dispatched only after its blank-line terminator. EOF in
		// a half-written terminal event must remain an incomplete attempt.
		if p.protocol == "/v1/messages" {
			keys := sortedKeys(p.blocks)
			var content []any
			for _, i := range keys {
				content = append(content, p.blocks[i])
			}
			p.output = contentAtoms("assistant", content)
		}
		if p.protocol == "/chat/completions" {
			p.output = contentAtoms("assistant", p.chatText.String())
			for _, i := range sortedKeys(p.chatTools) {
				t := p.chatTools[i]
				var input any
				if decode([]byte(str(t["arguments"])), &input) != nil {
					input = t["arguments"]
				}
				p.output = append(p.output, makeAtom("assistant", object{"type": "tool_use", "id": t["id"], "name": t["name"], "input": input}))
			}
		}
		return
	}
	var e object
	if decode(p.buffer, &e) != nil {
		p.problem = "Response body could not be decoded"
		return
	}
	if errorObject := obj(e["error"]); errorObject != nil {
		p.problem = str(errorObject["message"])
		return
	}
	switch p.protocol {
	case "/v1/messages":
		p.id = str(e["id"])
		p.stop = str(e["stop_reason"])
		p.terminal = p.stop != ""
		p.anthropicUsage(obj(e["usage"]))
		p.output = contentAtoms("assistant", e["content"])
	case "/responses":
		p.readResponse(e)
	case "/chat/completions":
		p.id = str(e["id"])
		p.openAIUsage(obj(e["usage"]), true)
		for _, v := range arr(e["choices"]) {
			c := obj(v)
			if integer(c["index"]) != 0 {
				continue
			}
			p.stop = str(c["finish_reason"])
			p.terminal = p.stop != ""
			m := obj(c["message"])
			p.output = contentAtoms("assistant", m["content"])
			for _, v := range arr(m["tool_calls"]) {
				call := obj(v)
				fn := obj(call["function"])
				var input any
				_ = decode([]byte(str(fn["arguments"])), &input)
				p.output = append(p.output, makeAtom("assistant", object{"type": "tool_use", "id": call["id"], "name": fn["name"], "input": input}))
			}
		}
	}
}
func sortedKeys[T any](m map[int]T) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// Capture belongs to a single HTTP handler. Chunk parsing is lock-free; only
// small state publications lock the store. Disabling capture never closes the
// inference transport or changes its request context.
type Capture struct {
	store      *Store
	turn       *turn
	protocol   string
	started    time.Time
	head       *node
	model      Model
	parser     *responseParser
	status     int
	first      bool
	writeError error
	finished   bool
}

func (c *Capture) SetModel(id, name string) {
	if c != nil {
		if name == "" {
			name = id
		}
		c.model = Model{ID: id, Name: name}
	}
}
func (c *Capture) Wrap(w http.ResponseWriter) http.ResponseWriter {
	return &captureWriter{ResponseWriter: w, capture: c}
}
func (c *Capture) observe(data []byte, stream bool) {
	if !c.first && len(data) > 0 {
		c.first = true
		s := c.store
		s.mu.Lock()
		if !s.closed && c.turn.FirstChunkMS == nil {
			ms := max(0, time.Now().UnixMilli()-c.turn.StartedAt)
			c.turn.FirstChunkMS = &ms
			s.revision++
		}
		s.mu.Unlock()
	}
	c.parser.feed(data, stream)
}
func (c *Capture) Finish(now time.Time, requestError error) {
	if c == nil || c.finished {
		return
	}
	c.finished = true
	c.parser.finish()
	s := c.store
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	t := c.turn
	p := c.parser
	t.inflight--
	if !slices.Contains(t.models, c.model) {
		t.models = append(t.models, c.model)
	}
	t.ModelID = c.model.ID
	t.ModelName = c.model.Name
	t.Usage.add(p.usage)
	status := c.status
	if status == 0 {
		status = 200
	}
	s.bytes += 128
	t.requests = append(t.requests, RequestSummary{ModelID: c.model.ID, StartedAt: c.started.UnixMilli(), EndedAt: now.UnixMilli(), Status: status, StopReason: p.stop, Usage: p.usage})
	problem := p.problem
	if requestError != nil {
		problem = "Request canceled or interrupted"
	}
	if c.writeError != nil {
		problem = "Client disconnected during response"
	}
	if status >= 300 {
		problem = fmt.Sprintf("HTTP %d", status)
		if p.problem != "" {
			problem += " · " + p.problem
		}
	}
	if problem == "" && !p.terminal {
		problem = "Response ended before its terminal event"
	}
	if problem == "" && (p.stop == "max_tokens" || p.stop == "length" || p.stop == "refusal" || p.stop == "content_filter") {
		problem = "Response stopped: " + p.stop
	}
	var reply []any
	for _, a := range p.output {
		var value object
		_ = decode(a.data, &value)
		if a.kind == "tool_use" || a.kind == "server_tool_use" {
			id := str(value["id"])
			found := false
			for _, call := range t.calls {
				if call.ID == id && id != "" {
					found = true
					break
				}
			}
			if !found {
				s.bytes += 192
				t.calls = append(t.calls, ToolCall{ID: id, Name: str(value["name"]), Input: s.intern(raw(value["input"])).data, StartedAt: now.UnixMilli()})
			}
		}
		if strings.HasSuffix(a.kind, "tool_result") {
			for i := range t.calls {
				if t.calls[i].ID == str(value["tool_use_id"]) {
					t.calls[i].Result = s.intern(raw(value["content"])).data
					t.calls[i].EndedAt = now.UnixMilli()
					t.calls[i].IsError, _ = value["is_error"].(bool)
				}
			}
		}
		if a.kind == "text" {
			reply = append(reply, value)
		}
		c.head = s.appendNode(c.head, a)
		if c.head.owner == nil {
			c.head.owner = t
		}
	}
	t.ToolCount = len(t.calls)
	if p.clipped {
		t.CaptureNotice = "Large response content was clipped in the visualizer. The forwarded response was unchanged."
	}
	t.head = c.head
	t.session.head = c.head
	// The harness executes actual tool blocks even if stop_reason says end_turn.
	for _, a := range p.output {
		if a.kind == "tool_use" {
			p.stop = "tool_use"
			break
		}
	}
	if problem != "" {
		t.Status = "failed"
		t.Error = problem
		t.EndedAt = now.UnixMilli()
	} else if p.stop == "tool_use" || p.stop == "tool_calls" || p.stop == "function_call" || p.stop == "pause_turn" {
		t.Status = "waiting"
	} else {
		t.Status = "completed"
		t.EndedAt = now.UnixMilli()
		t.reply = s.intern(raw(reply))
	}
	if p.id != "" && p.terminal {
		if s.responses[t.session.profile+":"+p.id] == nil {
			s.bytes += 128
		}
		s.responses[t.session.profile+":"+p.id] = c.head
	}
	s.resolveHelperAnchors(t.session)
	s.revision++
	s.prune()
}

type captureWriter struct {
	http.ResponseWriter
	capture *Capture
}

func (w *captureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *captureWriter) WriteHeader(status int) {
	if status >= 200 && w.capture.status == 0 {
		w.capture.status = status
	}
	w.ResponseWriter.WriteHeader(status)
}
func (w *captureWriter) Write(b []byte) (int, error) {
	if w.capture.status == 0 {
		w.capture.status = 200
	}
	n, err := w.ResponseWriter.Write(b)
	if n > 0 {
		w.capture.observe(b[:n], strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream"))
	}
	if err != nil {
		w.capture.writeError = err
	}
	return n, err
}
func (w *captureWriter) FlushError() error {
	err := http.NewResponseController(w.ResponseWriter).Flush()
	if err != nil {
		w.capture.writeError = err
	}
	return err
}
func (w *captureWriter) Flush() { _ = w.FlushError() }
