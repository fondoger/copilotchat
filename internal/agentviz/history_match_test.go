package agentviz

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestRotatingSessionIDsKeepCanceledTurnThenForkEditedInstruction(t *testing.T) {
	s := New()
	history := []any{user("First task")}
	first := begin(t, s, "wire-a", history, epoch)
	thinking := object{"type": "thinking", "thinking": "", "signature": "original encrypted reasoning"}
	complete(first, epoch.Add(time.Second), "end_turn", thinking, textBlock("First answer"))
	// The client replays visible content without the old encrypted reasoning.
	history = append(history, assistant(textBlock("First answer")), user("Second task"))
	second := begin(t, s, "wire-a", history, epoch.Add(2*time.Second))
	complete(second, epoch.Add(3*time.Second), "end_turn", textBlock("Second answer"))
	base := append(append([]any{}, history...), assistant(textBlock("Second answer")))
	commonPrefix := "1. Fork connectors should have one elbow. " + strings.Repeat("same visible preview ", 60)
	interrupted := begin(t, s, "wire-b", append(append([]any{}, base...), user(commonPrefix+"Keep the old requirement.")), epoch.Add(4*time.Second))
	interrupted.status = 502
	interrupted.Finish(epoch.Add(5*time.Second), context.Canceled)
	if interrupted.turn.session != first.turn.session || interrupted.turn.Status != "failed" {
		t.Fatal("rotating ID or reasoning omission split the canceled main turn")
	}
	rewound := begin(t, s, "wire-c", append(append([]any{}, base...), user(commonPrefix+"Use the edited requirement.")), epoch.Add(6*time.Second))
	complete(rewound, epoch.Add(7*time.Second), "end_turn", textBlock("Edited response"))
	snapshot := s.Snapshot(epoch.Add(8 * time.Second))
	if len(snapshot.Sessions) != 2 || len(first.turn.session.turns) != 3 || rewound.turn.session.parent != first.turn.session.id || rewound.turn.session.forkTurn != second.turn.ID {
		t.Fatal("expected main including failed attempt plus one rewind branch")
	}
	if interrupted.turn.InstructionPreview != rewound.turn.InstructionPreview {
		t.Fatal("fixture must share its full preview; identity must use the complete instruction")
	}
}

func TestRewindReturningToOriginalWireIDKeepsCanceledTurn(t *testing.T) {
	s := New()
	root := begin(t, s, "original-id", []any{user("Start")}, epoch)
	complete(root, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	prefix := []any{user("Start"), assistant(textBlock("Ready"))}
	failed := begin(t, s, "sdk-invocation-id", append(append([]any{}, prefix...), user("Same beginning; original ending")), epoch.Add(2*time.Second))
	failed.Finish(epoch.Add(3*time.Second), context.Canceled)
	edited := begin(t, s, "original-id", append(prefix, user("Same beginning; edited ending")), epoch.Add(4*time.Second))
	complete(edited, epoch.Add(5*time.Second), "end_turn", textBlock("Edited"))
	if failed.turn.session != root.turn.session || edited.turn.session.parent != root.turn.session.id || len(root.turn.session.turns) != 2 {
		t.Fatal("returning wire ID moved the canceled main attempt into a child")
	}
}

func TestSameWireRewindKeepsFailedAttemptOnOriginalRow(t *testing.T) {
	s := New()
	first := begin(t, s, "wire", []any{user("Start")}, epoch)
	complete(first, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	prefix := []any{user("Start"), assistant(textBlock("Ready"))}
	failed := begin(t, s, "wire", append(append([]any{}, prefix...), user("Task original")), epoch.Add(2*time.Second))
	failed.Finish(epoch.Add(3*time.Second), context.Canceled)
	branch := begin(t, s, "wire", append(append([]any{}, prefix...), user("Task edited")), epoch.Add(4*time.Second))
	complete(branch, epoch.Add(5*time.Second), "end_turn", textBlock("Edited"))
	if failed.turn.session != first.turn.session || branch.turn.session == first.turn.session || branch.turn.session.parent != first.turn.session.id {
		t.Fatal("same-wire edit moved the interrupted attempt off the original row")
	}
}

func TestOriginalClientResumingSplitsProvisionalTipInsteadOfForkingMain(t *testing.T) {
	for _, pending := range []bool{false, true} {
		name := "finished child"
		if pending {
			name = "pending child"
		}
		t.Run(name, func(t *testing.T) {
			s := New()
			root := begin(t, s, "main", []any{user("Start")}, epoch)
			complete(root, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
			prefix := []any{user("Start"), assistant(textBlock("Ready"))}
			fork := begin(t, s, "new-wire", append(append([]any{}, prefix...), user("Independent fork work")), epoch.Add(2*time.Second))
			if !pending {
				complete(fork, epoch.Add(3*time.Second), "end_turn", textBlock("Fork result"))
			}
			main := begin(t, s, "main", append(append([]any{}, prefix...), user("Continue original")), epoch.Add(4*time.Second))
			complete(main, epoch.Add(5*time.Second), "end_turn", textBlock("Main result"))
			if pending {
				complete(fork, epoch.Add(6*time.Second), "end_turn", textBlock("Fork result"))
			}
			if main.turn.session != root.turn.session || fork.turn.session == root.turn.session || fork.turn.session.parent != root.turn.session.id || fork.turn.session.forkTurn != root.turn.ID {
				t.Fatal("arrival order gave ownership of primary row to the fork")
			}
			if fork.turn.Number != 1 || main.turn.Number != 2 || len(s.Snapshot(epoch.Add(7*time.Second)).Sessions) != 2 {
				t.Fatal("splitting changed the visible turn history")
			}
		})
	}
}

func TestSplittingNewWireSuffixMovesItsHelperAncestry(t *testing.T) {
	s := New()
	root := begin(t, s, "main", []any{user("Start")}, epoch)
	complete(root, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	prefix := []any{user("Start"), assistant(textBlock("Ready"))}
	branchHistory := append(append([]any{}, prefix...), user("Branch task"))
	branch := begin(t, s, "branch", branchHistory, epoch.Add(2*time.Second))
	complete(branch, epoch.Add(3*time.Second), "end_turn", textBlock("Branch answer"))
	helper := begin(t, s, "branch", append(branchHistory, assistant(textBlock("Branch answer")), suggestion("Predict.")), epoch.Add(4*time.Second))
	complete(helper, epoch.Add(5*time.Second), "end_turn")
	main := begin(t, s, "main", append(prefix, user("Main continues")), epoch.Add(6*time.Second))
	complete(main, epoch.Add(7*time.Second), "end_turn", textBlock("Continued"))
	if helper.turn.session.parent != branch.turn.session.id || helper.turn.session.forkTurn != branch.turn.ID || branch.turn.session.parent != root.turn.session.id {
		t.Fatal("helper stayed attached to the wrong row after its parent split")
	}
}

func TestExplicitSubagentAtTipNeverMergesWithMain(t *testing.T) {
	s := New()
	root := begin(t, s, "main", []any{user("Start")}, epoch)
	complete(root, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	history := []any{user("Start"), assistant(textBlock("Ready")), user("Child task")}
	h := http.Header{"X-Claude-Code-Session-Id": {"main"}, "X-Claude-Code-Agent-Id": {"child"}}
	child := s.Begin("profile", "/v1/messages", h, raw(object{"model": "m", "messages": history}), epoch.Add(2*time.Second))
	complete(child, epoch.Add(3*time.Second), "end_turn", textBlock("Child answer"))
	if child.turn.session == root.turn.session || child.turn.session.parent != root.turn.session.id {
		t.Fatal("explicit agent identity was ignored")
	}
}
