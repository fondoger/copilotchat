package agentviz

import "fmt"

func (s *Store) newSession(p *parsedRequest, profile, purpose string) *session {
	s.next++
	id := fmt.Sprintf("s%d", s.next)
	sess := &session{id: id, profile: profile, identity: p.identity, client: p.client, group: id, purpose: purpose, explicitBranch: p.explicitBranch}
	s.sessions = append(s.sessions, sess)
	return sess
}

// A wire session can contain a main conversation and cache-sharing helper calls.
// Purpose isolates their mutable heads; HTTP arrival order never chooses which
// lane is primary. Prefix ancestry still distinguishes actual rewind/fork edits.
func (s *Store) sessionFor(p *parsedRequest, profile string, head, forkBase *node) (*session, *node) {
	s.splitResumedPrimary(p, profile, head)
	var chosen, parent *session
	var prefix, parentPrefix *node
	best := -1
	for _, candidate := range s.sessions {
		if candidate.profile != profile {
			continue
		}
		shared, covered := matchHistory(candidate.head, head)
		if candidate.purpose == "" {
			if t := sharedTurn(shared); t != nil && t.session.purpose == "" && depth(shared) > depth(parentPrefix) {
				parent, parentPrefix = t.session, shared
			}
		}
		if candidate.purpose != p.purpose {
			continue
		}
		knownIdentity := candidate.ownsIdentity(p.identity)
		crossIdentity := p.identity != "" && !knownIdentity
		if crossIdentity && (p.purpose != "" || p.explicitBranch || candidate.explicitBranch || p.client != candidate.client) {
			continue
		}
		last := latest(candidate)
		if last == nil {
			// An unseen main lane reserved by an earlier helper owns no captured
			// work yet. Its first real request can precede the helper's history.
			if knownIdentity && (depth(shared) > best || chosen == nil) {
				chosen, prefix, best = candidate, nil, depth(shared)
			}
			continue
		}
		if last.inflight > 0 {
			continue
		}
		if p.purpose != "" && last.Status == "completed" && head == last.requestHead {
			continue
		}
		matches := covered
		if !matches && last.Status == "failed" {
			if retryPrefix, retry := matchHistory(last.requestHead, head); retry {
				shared, matches = retryPrefix, true
			}
		}
		if !matches && p.identity != "" && last.Status == "waiting" {
			// A recent unchanged tool ID AND arguments prove continuity through
			// tool-result budgeting or old-media removal. Rewinds lose that suffix.
			if anchor := matchingToolAnchor(candidate.head, head); anchor != nil {
				shared, matches = anchor, true
			}
		}
		if !matches && p.compacted && knownIdentity && p.purpose == "" {
			shared, matches = nil, true
		}
		if !matches || crossIdentity && sharedTurn(shared) == nil {
			continue
		}
		if p.identity == "" && sharedTurn(shared) == nil && last.Status != "failed" {
			continue
		}
		if depth(shared) > best || depth(shared) == best && chosen != nil && candidate.lastStart > chosen.lastStart {
			chosen, prefix, best = candidate, shared, depth(shared)
		}
	}
	if chosen != nil {
		return chosen, prefix
	}
	if p.purpose != "" {
		owner := s.helperOwner(p, profile, forkBase)
		child := s.newSession(p, profile, p.purpose)
		child.parent, child.group, child.forkBase = owner.id, owner.group, forkBase
		if anchor := sessionAnchor(forkBase, owner); anchor != nil {
			child.forkTurn = anchor.ID
		}
		return child, forkBase
	}
	chosen = s.newSession(p, profile, "")
	if parent != nil {
		shared := sharedTurn(parentPrefix)
		chosen.parent, chosen.group, chosen.forkTurn = parent.id, parent.group, shared.ID
		return chosen, parentPrefix
	}
	if p.parentIdentity != "" {
		for _, candidate := range s.sessions {
			if candidate != chosen && candidate.profile == profile && candidate.purpose == "" && candidate.ownsIdentity(p.parentIdentity) && (parent == nil || candidate.lastStart > parent.lastStart) {
				parent = candidate
			}
		}
		if parent != nil {
			chosen.parent, chosen.group = parent.id, parent.group
			if shared := latest(parent); shared != nil {
				chosen.forkTurn = shared.ID
			}
		}
	}
	return chosen, nil
}

func (s *Store) helperOwner(p *parsedRequest, profile string, base *node) *session {
	var owner *session
	best := -1
	for _, candidate := range s.sessions {
		if candidate.profile != profile || !candidate.ownsIdentity(p.identity) || candidate.purpose != "" {
			continue
		}
		shared, _ := matchHistory(candidate.head, base)
		overlap := depth(shared)
		if overlap > best || overlap == best && owner != nil && candidate.lastStart > owner.lastStart {
			owner, best = candidate, overlap
		}
	}
	if owner != nil {
		return owner
	}
	// No fabricated historical turns: keep just the immutable shared prefix.
	// The row becomes visible once a main request is actually observed.
	owner = s.newSession(p, profile, "")
	owner.head = base
	return owner
}

func sessionAnchor(base *node, owner *session) *turn {
	for n := base; n != nil; n = n.parent {
		if n.owner != nil && n.owner.session == owner {
			return n.owner
		}
	}
	return nil
}

func (s *Store) resolveHelperAnchors(owner *session) {
	if owner.purpose != "" {
		return
	}
	for _, child := range s.sessions {
		if child.purpose != "" && child.parent == owner.id {
			if anchor := sessionAnchor(child.forkBase, owner); anchor != nil {
				child.forkTurn = anchor.ID
			}
		}
	}
}
