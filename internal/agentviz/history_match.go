package agentviz

import "slices"

func reasoningNode(n *node) bool {
	return n.value.role == "assistant" && (n.value.kind == "thinking" || n.value.kind == "redacted_thinking" || n.value.kind == "reasoning")
}

func visibleHistory(n *node) []*node {
	var nodes []*node
	for ; n != nil; n = n.parent {
		if !reasoningNode(n) {
			nodes = append(nodes, n)
		}
	}
	slices.Reverse(nodes)
	return nodes
}

// Compare complete visible history, never instruction previews or cache rates.
// Clients can omit old reasoning blocks or replay them in a different grouping.
// Their bytes/signatures remain stored and forwarded unchanged; only this lane
// comparison projects them out. User text, tool IDs/arguments/results and media
// digests must still match exactly, with JSON number spelling preserved.
func matchHistory(old, incoming *node) (prefix *node, covered bool) {
	prefix = common(old, incoming)
	if old == nil {
		return prefix, false
	}
	if prefix == old {
		return prefix, true
	}
	a, b := visibleHistory(old), visibleHistory(incoming)
	matched := 0
	for matched < len(a) && matched < len(b) {
		x, y := a[matched], b[matched]
		if x.value.role != y.value.role || x.payload.key != y.payload.key {
			break
		}
		if y.owner == nil && x.owner != nil {
			y.owner = x.owner
		}
		prefix = y
		matched++
	}
	return prefix, len(a) > 0 && matched == len(a)
}

func (sess *session) ownsIdentity(id string) bool {
	if id == "" {
		return false
	}
	if sess.identity == id {
		return true
	}
	for _, t := range sess.turns {
		if t.identity == id {
			return true
		}
	}
	return false
}

// A fresh wire ID at the current tip may be a resumed SDK invocation or a fork.
// Until the original client continues elsewhere their visible history is the
// same. When it does, move the new-ID suffix to a child row, preserving turn IDs
// and the original primary row rather than making arrival order own the branch.
func (s *Store) splitResumedPrimary(p *parsedRequest, profile string, incoming *node) {
	if p.purpose != "" || p.explicitBranch || p.identity == "" {
		return
	}
	for _, sess := range slices.Clone(s.sessions) {
		if sess.profile != profile || sess.purpose != "" || !sess.ownsIdentity(p.identity) || len(sess.turns) < 2 {
			continue
		}
		// Keep an interruption on the lane that accepted it. Replacing that
		// failed instruction is rewind even if the client returns to an old ID.
		if latest(sess).Status == "failed" {
			continue
		}
		if _, covered := matchHistory(sess.head, incoming); covered {
			continue
		}
		shared := -1
		for i := len(sess.turns) - 1; i >= 0; i-- {
			if _, covered := matchHistory(sess.turns[i].head, incoming); covered {
				shared = i
				break
			}
		}
		if shared < 0 || shared == len(sess.turns)-1 {
			continue
		}
		moved := sess.turns[shared+1:]
		canSplit := true
		for _, t := range moved {
			if t.identity == "" || t.identity == p.identity || t.identity == sess.identity {
				canSplit = false
				break
			}
		}
		if !canSplit {
			continue
		}
		child := s.newSession(&parsedRequest{identity: moved[0].identity, client: sess.client}, profile, "")
		child.parent, child.group, child.forkTurn = sess.id, sess.group, sess.turns[shared].ID
		child.forkBase = sess.turns[shared].head
		child.turns = slices.Clone(moved)
		child.head = child.turns[len(child.turns)-1].head
		child.lastStart = child.turns[len(child.turns)-1].StartedAt
		label := []rune(moved[0].InstructionPreview)
		if len(label) > 100 {
			label = append(label[:100], '…')
		}
		child.label = string(label)
		if child.label == "" {
			child.label = child.client + " session"
		}
		firstNumber := moved[0].Number
		ids := map[string]bool{}
		for _, t := range child.turns {
			t.session = child
			t.SessionID = child.id
			t.Number = t.Number - firstNumber + 1
			ids[t.ID] = true
		}
		sess.turns = slices.Clone(sess.turns[:shared+1])
		sess.head = sess.turns[shared].head
		sess.lastStart = sess.turns[shared].StartedAt
		for _, descendant := range s.sessions {
			if descendant == child || descendant.parent != sess.id {
				continue
			}
			if ids[descendant.forkTurn] {
				descendant.parent = child.id
				continue
			}
			if anchor := sessionAnchor(descendant.forkBase, child); anchor != nil {
				descendant.parent = child.id
				descendant.forkTurn = anchor.ID
			}
		}
	}
}
