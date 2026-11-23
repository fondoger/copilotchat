package agentviz

import (
	"cmp"
	"slices"
	"time"
)

func (s *Store) Snapshot(now time.Time) Snapshot {
	s.mu.Lock()
	out := Snapshot{Revision: s.revision, StartedAt: s.started.UnixMilli(), MemoryBytes: s.bytes, DroppedSessions: s.dropped, Sessions: []SessionSummary{}, Columns: []Column{}}
	for _, sess := range s.sessions {
		if len(sess.turns) == 0 {
			continue
		}
		r := SessionSummary{ID: sess.id, Label: sess.label, Client: sess.client, GroupID: sess.group, ParentID: sess.parent, ForkTurnID: sess.forkTurn, Kind: sess.purpose, LastStart: sess.lastStart, Turns: []TurnSummary{}}
		for _, t := range sess.turns {
			r.Turns = append(r.Turns, summary(t, now))
		}
		out.Sessions = append(out.Sessions, r)
	}
	s.mu.Unlock()
	layout(&out)
	return out
}

type slot struct {
	start, end int64
	turns      []*TurnSummary
}

// layout uses event columns, not time buckets. A session always advances; a
// parallel session can occupy the closest existing column. New activity after
// an established timeline extends it rather than folding back into its last turn.
func layout(out *Snapshot) {
	chronological := make([]*SessionSummary, 0, len(out.Sessions))
	for i := range out.Sessions {
		sess := &out.Sessions[i]
		if len(sess.Turns) > 0 {
			chronological = append(chronological, sess)
		}
	}
	slices.SortFunc(chronological, func(a, b *SessionSummary) int {
		if c := cmp.Compare(a.Turns[0].StartedAt, b.Turns[0].StartedAt); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
	var slots []*slot
	turnSlot := map[string]*slot{}
	for _, sess := range chronological {
		var prev *slot
		for i := range sess.Turns {
			t := &sess.Turns[i]
			previous := -1
			if prev != nil {
				previous = slices.Index(slots, prev)
			}
			if i == 0 && sess.ForkTurnID != "" {
				if parent := turnSlot[sess.ForkTurnID]; parent != nil {
					previous = slices.Index(slots, parent)
				}
			}
			nearest := -1
			distance := int64(1<<63 - 1)
			for j, col := range slots {
				d := abs(t.StartedAt - col.start)
				if d < distance {
					nearest, distance = j, d
				}
			}
			align := nearest > previous && nearest >= 0 && distance <= int64(3*time.Minute/time.Millisecond)
			if len(slots) == 1 && previous < 0 && t.StartedAt >= slots[0].start && t.StartedAt-slots[0].end <= int64(3*time.Minute/time.Millisecond) {
				align = true
			}
			// With one existing first turn, allow the startup grace period. Once
			// there is a timeline, only temporally overlapping sessions align.
			if len(slots) > 1 && t.StartedAt > slots[len(slots)-1].start {
				align = false
			}
			if align {
				prev = slots[nearest]
				prev.turns = append(prev.turns, t)
				prev.end = max(prev.end, turnEnd(*t))
				prev.start = min(prev.start, t.StartedAt)
			} else {
				at := len(slots)
				for j, col := range slots {
					if j > previous && col.start > t.StartedAt {
						at = j
						break
					}
				}
				at = max(at, previous+1)
				prev = &slot{start: t.StartedAt, end: turnEnd(*t), turns: []*TurnSummary{t}}
				slots = slices.Insert(slots, at, prev)
			}
			turnSlot[t.ID] = prev
		}
	}
	out.Columns = []Column{}
	var lastEnd int64
	for i, col := range slots {
		if i > 0 {
			gap := gapColumns(col.start - lastEnd)
			for range gap {
				out.Columns = append(out.Columns, Column{Start: lastEnd, End: col.start, Gap: true})
			}
		}
		index := len(out.Columns)
		out.Columns = append(out.Columns, Column{Start: col.start, End: col.end})
		for _, t := range col.turns {
			t.Column = index
		}
		lastEnd = max(lastEnd, col.end)
	}
	groupIdleForks(out.Sessions)
	groupLatest := map[string]int64{}
	for i := range out.Sessions {
		sess := &out.Sessions[i]
		groupLatest[sess.GroupID] = max(groupLatest[sess.GroupID], sess.LastStart)
		for j := range sess.Turns {
			sess.Turns[j].Level = cacheLevel(sess.Turns[j].CacheHitRate)
		}
	}
	slices.SortFunc(out.Sessions, func(a, b SessionSummary) int {
		if a.GroupID != b.GroupID {
			if c := cmp.Compare(groupLatest[b.GroupID], groupLatest[a.GroupID]); c != 0 {
				return c
			}
			return cmp.Compare(a.GroupID, b.GroupID)
		}
		if c := cmp.Compare(b.LastStart, a.LastStart); c != 0 {
			return c
		}
		return cmp.Compare(a.ID, b.ID)
	})
}

// Grouping is a view projection, separate from immutable fork ancestry and the
// shared-memory retention family. Old idle rows keep their fork provenance but
// sort independently. A resumed row rejoins its family on the next snapshot.
func groupIdleForks(sessions []SessionSummary) {
	families := map[string][]*SessionSummary{}
	for i := range sessions {
		sess := &sessions[i]
		if sess.ParentID == "" {
			continue
		}
		active := false
		for _, t := range sess.Turns {
			if t.Status == "running" || t.Status == "waiting" {
				active = true
				break
			}
		}
		if !active {
			families[sess.GroupID] = append(families[sess.GroupID], sess)
		}
	}
	for _, forks := range families {
		slices.SortFunc(forks, func(a, b *SessionSummary) int {
			if n := cmp.Compare(b.LastStart, a.LastStart); n != 0 {
				return n
			}
			return cmp.Compare(a.ID, b.ID)
		})
		for _, sess := range forks[min(2, len(forks)):] {
			sess.Detached, sess.GroupID = true, sess.ID
		}
	}
}

func cacheLevel(rate float64) int {
	switch {
	case rate >= .95:
		return 4
	case rate >= .90:
		return 3
	case rate >= .75:
		return 2
	default:
		return 1
	}
}

func turnEnd(t TurnSummary) int64 {
	if t.EndedAt > 0 {
		return t.EndedAt
	}
	return t.StartedAt + t.DurationMS
}
func abs(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}
func gapColumns(ms int64) int {
	switch {
	case ms > int64(30*time.Minute/time.Millisecond):
		return 3
	case ms > int64(10*time.Minute/time.Millisecond):
		return 2
	case ms > int64(3*time.Minute/time.Millisecond):
		return 1
	}
	return 0
}
