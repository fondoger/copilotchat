package agentviz

import (
	"fmt"
	"testing"
	"time"
)

func snapshotSession(t *testing.T, snapshot Snapshot, id string) SessionSummary {
	t.Helper()
	for _, sess := range snapshot.Sessions {
		if sess.ID == id {
			return sess
		}
	}
	t.Fatalf("missing session %s", id)
	return SessionSummary{}
}

func TestOnlyTwoNewestIdleForksMoveWithTheirFamily(t *testing.T) {
	main := sessionAt("main", 0, 100)
	snapshot := Snapshot{Sessions: []SessionSummary{main, sessionAt("other", 25), sessionAt("recent-unrelated", 90)}}
	for i := 1; i <= 5; i++ {
		fork := sessionAt(fmt.Sprint("fork", i), i*10)
		fork.ParentID = "main"
		fork.GroupID = "main"
		fork.ForkTurnID = "main-0"
		snapshot.Sessions = append(snapshot.Sessions, fork)
	}
	// An older fork still working does not consume an idle-row slot.
	active := sessionAt("active", 1)
	active.ParentID = "main"
	active.GroupID = "main"
	active.ForkTurnID = "main-0"
	active.Turns[0].Status = "waiting"
	snapshot.Sessions = append(snapshot.Sessions, active)
	layout(&snapshot)
	family := 0
	for _, sess := range snapshot.Sessions {
		if sess.GroupID == "main" {
			family++
		}
		if sess.ID == "fork4" || sess.ID == "fork5" || sess.ID == "active" {
			if sess.Detached || sess.GroupID != "main" {
				t.Fatalf("recent/active fork detached: %s", sess.ID)
			}
		}
		if sess.ID == "fork1" || sess.ID == "fork2" || sess.ID == "fork3" {
			if !sess.Detached || sess.GroupID != sess.ID || sess.ParentID != "main" || sess.ForkTurnID != "main-0" {
				t.Fatalf("old fork lost independent sorting or provenance: %+v", sess)
			}
		}
	}
	if family != 4 {
		t.Fatalf("family rows=%d, want main + active + two idle", family)
	}
	for i := 0; i < 4; i++ {
		if snapshot.Sessions[i].GroupID != "main" {
			t.Fatal("family was interleaved")
		}
	}
	want := []string{"recent-unrelated", "fork3", "other", "fork2", "fork1"}
	for i, id := range want {
		if snapshot.Sessions[i+4].ID != id {
			t.Fatalf("independent order at %d: %s want %s", i, snapshot.Sessions[i+4].ID, id)
		}
	}
}

func TestDetachedForkResumeRejoinsWithoutChangingStoredAncestry(t *testing.T) {
	s := New()
	root := begin(t, s, "main", []any{user("Start")}, epoch)
	complete(root, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	prefix := []any{user("Start"), assistant(textBlock("Ready"))}
	var forks []*Capture
	for i := 0; i < 4; i++ {
		history := append(append([]any{}, prefix...), user(fmt.Sprint("Fork ", i)))
		c := begin(t, s, fmt.Sprint("child", i), history, epoch.Add(time.Duration(i+2)*time.Second))
		complete(c, epoch.Add(time.Duration(i+2)*time.Second+time.Millisecond), "end_turn", textBlock(fmt.Sprint("Reply ", i)))
		forks = append(forks, c)
	}
	// The original client resumes, proving the new-ID tip was a parallel fork.
	continued := begin(t, s, "main", append(append([]any{}, prefix...), user("Continue main")), epoch.Add(8*time.Second))
	complete(continued, epoch.Add(9*time.Second), "end_turn", textBlock("Main continued"))
	old := forks[0].turn.session
	if !snapshotSession(t, s.Snapshot(epoch.Add(10*time.Second)), old.id).Detached {
		t.Fatal("older idle fork still attached")
	}
	for _, sess := range s.sessions {
		if sess.group != root.turn.session.id {
			t.Fatal("view grouping mutated retention ancestry")
		}
	}
	history := append(append([]any{}, prefix...), user("Fork 0"), assistant(textBlock("Reply 0")), user("Resume"))
	resumed := begin(t, s, "child0", history, epoch.Add(20*time.Second))
	if resumed.turn.session != old {
		t.Fatal("detached row lost its session identity")
	}
	row := snapshotSession(t, s.Snapshot(epoch.Add(21*time.Second)), old.id)
	if row.Detached || row.GroupID != root.turn.session.id {
		t.Fatal("active fork did not rejoin its family")
	}
	complete(resumed, epoch.Add(22*time.Second), "end_turn", textBlock("Resumed"))
	snapshot := s.Snapshot(epoch.Add(23 * time.Second))
	if snapshotSession(t, snapshot, old.id).Detached || !snapshotSession(t, snapshot, forks[2].turn.session.id).Detached {
		t.Fatal("newly completed fork did not replace the oldest attached idle row")
	}
}
