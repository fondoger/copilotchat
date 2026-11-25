package agentviz

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	defaultMaxBytes = 64 << 20
	defaultMaxTurns = 4096
)

type blob struct {
	key  string
	data json.RawMessage
}
type nodeKey struct {
	parent        uint64
	role, payload string
}
type node struct {
	id      uint64
	parent  *node
	depth   int
	value   atom
	payload *blob
	owner   *turn
}
type turn struct {
	TurnSummary
	session                           *session
	identity                          string
	system, tools, instruction, reply *blob
	models                            []Model
	calls                             []ToolCall
	requests                          []RequestSummary
	injections                        []*blob
	requestHead, head                 *node
	inflight                          int
}
type session struct {
	id, profile, identity, client, label, group, parent, forkTurn string
	purpose                                                       string
	explicitBranch                                                bool
	forkBase                                                      *node
	head                                                          *node
	turns                                                         []*turn
	lastStart                                                     int64
}

// Store serializes observer state only. It is never held while forwarding network
// traffic, rendering HTML, or encoding an HTTP response.
type Store struct {
	mu             sync.Mutex
	started        time.Time
	revision, next uint64
	blobs          map[string]*blob
	nodes          map[nodeKey]*node
	sessions       []*session
	turns          map[string]*turn
	responses      map[string]*node
	bytes          int64
	maxBytes       int64
	maxTurns       int
	dropped        int
	closed         bool
}

func New() *Store {
	return &Store{started: time.Now(), blobs: map[string]*blob{}, nodes: map[nodeKey]*node{}, turns: map[string]*turn{}, responses: map[string]*node{}, maxBytes: defaultMaxBytes, maxTurns: defaultMaxTurns}
}

func (s *Store) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	s.sessions = nil
	s.turns = map[string]*turn{}
	s.nodes = map[nodeKey]*node{}
	s.blobs = map[string]*blob{}
	s.responses = map[string]*node{}
	s.bytes = 0
	s.revision++
}
func (s *Store) Revision() uint64 { s.mu.Lock(); defer s.mu.Unlock(); return s.revision }
func (s *Store) intern(data json.RawMessage) *blob {
	if len(data) == 0 {
		data = json.RawMessage("null")
	}
	key := digest(data)
	if b := s.blobs[key]; b != nil {
		return b
	}
	b := &blob{key: key, data: slices.Clone(data)}
	s.blobs[key] = b
	s.bytes += int64(len(data)) + 128
	return b
}
func (s *Store) appendNode(parent *node, a atom) *node {
	b := s.intern(a.data)
	key := nodeKey{role: a.role, payload: b.key}
	depth := 1
	if parent != nil {
		key.parent = parent.id
		depth = parent.depth + 1
	}
	if n := s.nodes[key]; n != nil {
		return n
	}
	s.next++
	a.data = b.data
	n := &node{id: s.next, parent: parent, depth: depth, value: a, payload: b}
	s.nodes[key] = n
	s.bytes += 256
	return n
}
func common(a, b *node) *node {
	for a != nil && b != nil && a.depth > b.depth {
		a = a.parent
	}
	for a != nil && b != nil && b.depth > a.depth {
		b = b.parent
	}
	for a != b {
		if a == nil || b == nil {
			return nil
		}
		a = a.parent
		b = b.parent
	}
	return a
}
func depth(n *node) int {
	if n == nil {
		return 0
	}
	return n.depth
}
func tail(n, prefix *node) []atom {
	var a []atom
	for n != nil && n != prefix {
		a = append(a, n.value)
		n = n.parent
	}
	slices.Reverse(a)
	return a
}
func sharedTurn(n *node) *turn {
	for n != nil {
		if n.owner != nil {
			return n.owner
		}
		n = n.parent
	}
	return nil
}
func matchingToolAnchor(old, incoming *node) *node {
	// Only the latest assistant tool call is evidence of a current suffix. An
	// older shared tool call is also present in a genuine rewind/fork.
	for old != nil && (old.value.role != "assistant" || old.value.kind != "tool_use") {
		old = old.parent
	}
	if old == nil {
		return nil
	}
	for n := incoming; n != nil; n = n.parent {
		if n.value.role == "assistant" && n.value.kind == "tool_use" && n.payload.key == old.payload.key {
			return n
		}
	}
	return nil
}

func latest(sess *session) *turn {
	if len(sess.turns) == 0 {
		return nil
	}
	return sess.turns[len(sess.turns)-1]
}

// Begin sees the original client body, before provider model routing or protocol
// translation. Session identity narrows candidates; message ancestry splits forks
// even when rewind/subagents reuse a client session ID.
func (s *Store) Begin(profile, protocol string, h http.Header, body []byte, now time.Time) *Capture {
	parsed, err := parseRequest(body, h, protocol)
	if err != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed || s.bytes > s.maxBytes {
		return nil
	}
	var requestHead *node
	if parsed.previousID != "" {
		requestHead = s.responses[profile+":"+parsed.previousID]
	}
	var injections []atom
	var forkBase *node
	for i, a := range parsed.atoms {
		if i == parsed.forkAt {
			forkBase = requestHead
		}
		if a.role == "system" || a.role == "developer" {
			injections = append(injections, a)
			continue
		}
		requestHead = s.appendNode(requestHead, a)
	}
	if requestHead == nil {
		return nil
	}
	chosen, prefix := s.sessionFor(parsed, profile, requestHead, forkBase)
	delta := tail(requestHead, prefix)
	trigger := triggerFor(delta)
	last := latest(chosen)
	toolInput := toolContinuation(last, delta)
	if len(toolInput) != 0 {
		trigger = "continuation"
	}
	if parsed.scheduled && (last == nil || last.Status == "completed" || last.Status == "failed") {
		trigger = "scheduler"
	}
	continuing := last != nil && last.Status != "failed" && trigger == "continuation"
	if continuing { // Hook feedback and background notifications can reopen end_turn.
		last.Status = "running"
		last.EndedAt = 0
		last.tools = s.mergeTools(last.tools, parsed.tools)
	} else {
		number, attempt := len(chosen.turns)+1, 1
		if last != nil {
			number = last.Number + 1
			if last.Status == "failed" && (requestHead == last.requestHead || trigger != "user") {
				number, attempt = last.Number, last.Attempt+1
			}
			if last.Status == "running" || last.Status == "waiting" {
				last.Status = "failed"
				last.Error = "Interrupted by a new instruction"
				last.EndedAt = now.UnixMilli()
			}
		}
		s.next++
		id := fmt.Sprintf("t%d", s.next)
		if parsed.purpose != "" {
			trigger = parsed.purpose
		}
		last = &turn{TurnSummary: TurnSummary{ID: id, SessionID: chosen.id, Number: number, Attempt: attempt, Status: "running", Trigger: trigger, StartedAt: now.UnixMilli(), ModelID: parsed.model, ModelName: parsed.model}, session: chosen, identity: parsed.identity, system: s.intern(parsed.system), tools: s.intern(parsed.tools), models: []Model{}, calls: []ToolCall{}, requests: []RequestSummary{}, injections: []*blob{}}
		var instruction []any
		inputAtoms := currentInstruction(delta)
		for _, a := range inputAtoms {
			if a.role == "user" && a.kind != "tool_result" && (!synthetic(a) || trigger != "user") {
				var v any
				_ = decode(a.data, &v)
				instruction = append(instruction, v)
			}
		}
		if len(instruction) == 0 && attempt > 1 {
			last.instruction = latest(chosen).instruction
		} else {
			last.instruction = s.intern(raw(instruction))
		}
		last.InstructionPreview = previewInstruction(last.instruction.data)
		if chosen.label == "" && chosen.purpose == "suggestion" {
			chosen.label = "Prompt suggestion"
		}
		if chosen.label == "" {
			for _, a := range inputAtoms {
				if a.role == "user" && a.kind == "text" && !synthetic(a) {
					chosen.label = strings.Join(strings.Fields(atomText(a)), " ")
					break
				}
			}
			if chosen.label == "" {
				chosen.label = parsed.client + " session"
			}
			runes := []rune(chosen.label)
			if len(runes) > 100 {
				chosen.label = string(runes[:100]) + "…"
			}
		}
		chosen.turns = append(chosen.turns, last)
		s.turns[id] = last
		s.bytes += 512
		chosen.lastStart = last.StartedAt
	}
	for _, a := range append(delta, injections...) {
		if synthetic(a) {
			b := s.intern(a.data)
			if !slices.Contains(last.injections, b) {
				last.injections = append(last.injections, b)
			}
		}
		if a.kind == "tool_result" {
			var v object
			_ = decode(a.data, &v)
			for i := range last.calls {
				if last.calls[i].ID == str(v["tool_use_id"]) {
					last.calls[i].Result = s.intern(raw(v["content"])).data
					last.calls[i].IsError, _ = v["is_error"].(bool)
					last.calls[i].EndedAt = now.UnixMilli()
				}
			}
		}
	}
	for _, a := range toolInput {
		if a.role == "user" && a.kind != "tool_result" {
			b := s.intern(a.data)
			if !slices.Contains(last.injections, b) {
				last.injections = append(last.injections, b)
			}
		}
	}
	last.requestHead = requestHead
	last.head = requestHead
	chosen.head = requestHead
	last.inflight++
	last.RequestCount++
	s.revision++
	c := &Capture{store: s, turn: last, protocol: protocol, started: now, head: requestHead, model: Model{ID: parsed.model, Name: parsed.model}}
	c.parser = newResponseParser(protocol)
	s.prune()
	return c
}

func (s *Store) mergeTools(previous *blob, current json.RawMessage) *blob {
	if previous == nil {
		return s.intern(current)
	}
	if previous.key == digest(current) {
		return previous
	}
	var before, after []json.RawMessage
	if json.Unmarshal(previous.data, &before) != nil || json.Unmarshal(current, &after) != nil {
		return previous
	}
	seen := map[string]bool{}
	for _, tool := range before {
		seen[digest(tool)] = true
	}
	for _, tool := range after {
		if key := digest(tool); !seen[key] {
			before = append(before, tool)
			seen[key] = true
		}
	}
	return s.intern(raw(before))
}

func (s *Store) Detail(id string, now time.Time) (TurnDetail, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.turns[id]
	if t == nil {
		return TurnDetail{}, false
	}
	d := TurnDetail{TurnSummary: summary(t, now), Models: slices.Clone(t.models), Calls: slices.Clone(t.calls), Requests: slices.Clone(t.requests), Injections: []json.RawMessage{}}
	if t.system != nil {
		d.System = t.system.data
	}
	if t.tools != nil {
		d.Tools = t.tools.data
	}
	if t.instruction != nil {
		d.Instruction = t.instruction.data
	}
	if t.reply != nil {
		d.Reply = t.reply.data
	}
	for _, b := range t.injections {
		d.Injections = append(d.Injections, b.data)
	}
	return d, true
}
func summary(t *turn, now time.Time) TurnSummary {
	r := t.TurnSummary
	end := r.EndedAt
	if end == 0 {
		end = now.UnixMilli()
	}
	r.DurationMS = max(0, end-r.StartedAt)
	r.Level = cacheLevel(r.CacheHitRate)
	return r
}

// Evict whole idle families so no retained fork points into missing history.
// A single long-running family is allowed to finish; once idle it can be evicted.
func (s *Store) prune() {
	for s.bytes > s.maxBytes || len(s.turns) > s.maxTurns || len(s.sessions) > 256 {
		busy := map[string]bool{}
		for _, sess := range s.sessions {
			for _, t := range sess.turns {
				if t.inflight > 0 {
					busy[sess.group] = true
				}
			}
		}
		group := ""
		oldest := int64(1<<63 - 1)
		for _, sess := range s.sessions {
			if !busy[sess.group] && sess.lastStart < oldest {
				group = sess.group
				oldest = sess.lastStart
			}
		}
		if group == "" {
			return
		}
		s.sessions = slices.DeleteFunc(s.sessions, func(sess *session) bool {
			if sess.group != group {
				return false
			}
			for _, t := range sess.turns {
				delete(s.turns, t.ID)
			}
			s.dropped++
			return true
		})
		s.collect()
	}
}

func (s *Store) collect() {
	liveNodes := map[*node]bool{}
	liveBlobs := map[string]*blob{}
	markBlob := func(b *blob) {
		if b != nil {
			liveBlobs[b.key] = b
		}
	}
	markRaw := func(r json.RawMessage) {
		if len(r) > 0 {
			if b := s.blobs[digest(r)]; b != nil {
				liveBlobs[b.key] = b
			}
		}
	}
	markNode := func(n *node) {
		for n != nil && !liveNodes[n] {
			liveNodes[n] = true
			markBlob(n.payload)
			n = n.parent
		}
	}
	for _, sess := range s.sessions {
		markNode(sess.head)
		for _, t := range sess.turns {
			markNode(t.requestHead)
			markNode(t.head)
			markBlob(t.system)
			markBlob(t.tools)
			markBlob(t.instruction)
			markBlob(t.reply)
			for _, b := range t.injections {
				markBlob(b)
			}
			for _, c := range t.calls {
				markRaw(c.Input)
				markRaw(c.Result)
			}
		}
	}
	for key, n := range s.nodes {
		if !liveNodes[n] {
			delete(s.nodes, key)
		}
	}
	for key, n := range s.responses {
		if !liveNodes[n] {
			delete(s.responses, key)
		}
	}
	s.blobs = liveBlobs
	// A coincidentally identical response in an independent session may reuse a
	// trie node. Do not retain the evicted session through that node's owner.
	for n := range liveNodes {
		if n.owner != nil && s.turns[n.owner.ID] == nil {
			n.owner = nil
		}
	}
	for _, sess := range s.sessions {
		for _, t := range sess.turns {
			for n := t.head; n != nil && n != t.requestHead; n = n.parent {
				if n.owner == nil {
					n.owner = t
				}
			}
		}
	}
	s.bytes = int64(len(s.nodes))*256 + int64(len(s.turns))*512 + int64(len(s.responses))*128
	for _, b := range s.blobs {
		s.bytes += int64(len(b.data)) + 128
	}
	for _, sess := range s.sessions {
		for _, t := range sess.turns {
			s.bytes += int64(len(t.requests))*128 + int64(len(t.calls))*192
		}
	}
}
