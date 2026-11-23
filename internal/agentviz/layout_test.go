package agentviz

import (
	"fmt"
	"slices"
	"testing"
	"time"
)

func sessionAt(id string, seconds ...int) SessionSummary {
	s := SessionSummary{ID: id, GroupID: id}
	for i, second := range seconds {
		start := epoch.Add(time.Duration(second) * time.Second).UnixMilli()
		s.Turns = append(s.Turns, TurnSummary{ID: fmt.Sprintf("%s-%d", id, i), SessionID: id, StartedAt: start, EndedAt: start + 100, ToolCount: i, Status: "completed"})
		s.LastStart = start
	}
	return s
}
func positions(s Snapshot, id string) []int {
	for _, sess := range s.Sessions {
		if sess.ID == id {
			var a []int
			for _, turn := range sess.Turns {
				a = append(a, turn.Column)
			}
			return a
		}
	}
	return nil
}

func TestSharedColumnsAndRecency(t *testing.T) {
	s := Snapshot{Sessions: []SessionSummary{sessionAt("a", 0, 2, 8), sessionAt("b", 0, 2, 3, 8), sessionAt("c", 10, 11, 12)}}
	layout(&s)
	for id, want := range map[string][]int{"a": {0, 1, 3}, "b": {0, 1, 2, 3}, "c": {4, 5, 6}} {
		if got := positions(s, id); !slices.Equal(got, want) {
			t.Fatalf("%s columns=%v want %v", id, got, want)
		}
	}
	if s.Sessions[0].ID != "c" {
		t.Fatalf("newest row=%s", s.Sessions[0].ID)
	}
}
func TestNearestAlignmentDoesNotCollapseRapidTurns(t *testing.T) {
	s := Snapshot{Sessions: []SessionSummary{sessionAt("a", 0, 18, 36, 54, 72, 90, 108, 126, 144, 162), sessionAt("b", 60)}}
	layout(&s)
	if len(s.Columns) != 10 || positions(s, "b")[0] != 3 {
		t.Fatalf("alignment=%+v", s)
	}
	s = Snapshot{Sessions: []SessionSummary{sessionAt("a", 0), sessionAt("b", 179)}}
	layout(&s)
	if len(s.Columns) != 1 {
		t.Fatal("first-turn startup grace did not align")
	}
}
func TestIdleGapThresholdsUsePreviousEnd(t *testing.T) {
	for _, test := range []struct {
		gap    time.Duration
		blanks int
	}{{3 * time.Minute, 0}, {3*time.Minute + time.Millisecond, 1}, {10 * time.Minute, 1}, {10*time.Minute + time.Millisecond, 2}, {30 * time.Minute, 2}, {time.Hour, 3}} {
		t.Run(test.gap.String(), func(t *testing.T) {
			sess := sessionAt("a", 0)
			end := sess.Turns[0].StartedAt + int64(10*time.Minute/time.Millisecond)
			sess.Turns[0].EndedAt = end
			sess.Turns = append(sess.Turns, TurnSummary{ID: "a-second", StartedAt: end + test.gap.Milliseconds(), EndedAt: end + test.gap.Milliseconds() + 1})
			s := Snapshot{Sessions: []SessionSummary{sess}}
			layout(&s)
			if got := positions(s, "a")[1]; got != test.blanks+1 {
				t.Fatalf("column %d, want %d", got, test.blanks+1)
			}
		})
	}
}
func TestForkFamiliesMoveTogetherAndAfterAnchor(t *testing.T) {
	a := sessionAt("a", 0, 20)
	b := sessionAt("b", 1, 30)
	b.GroupID = "a"
	b.ParentID = "a"
	b.ForkTurnID = "a-0"
	s := Snapshot{Sessions: []SessionSummary{a, sessionAt("other", 25), b}}
	layout(&s)
	if s.Sessions[0].GroupID != "a" || s.Sessions[1].GroupID != "a" || s.Sessions[2].ID != "other" {
		t.Fatal("family was interleaved")
	}
	if positions(s, "b")[0] <= positions(s, "a")[0] {
		t.Fatal("fork did not advance beyond shared prefix")
	}
}
