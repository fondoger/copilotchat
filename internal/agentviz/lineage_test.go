package agentviz

import (
	"net/http"
	"testing"
	"time"
)

func suggestion(prompt string) object { return user(suggestionInstruction + "\n\n" + prompt) }

func TestSuggestionDoesNotAdvanceMainHistory(t *testing.T) {
	s := New()
	history := []any{user("Hello")}
	main := begin(t, s, "main", history, epoch)
	complete(main, epoch.Add(time.Second), "end_turn", textBlock("Welcome"))
	prefix := []any{user("Hello"), assistant(textBlock("Welcome"))}
	helper := begin(t, s, "main", append(prefix, suggestion("Predict the next input.")), epoch.Add(2*time.Second))
	if helper.turn.session == main.turn.session || helper.turn.Trigger != "suggestion" {
		t.Fatal("helper claimed the primary lane")
	}
	complete(helper, epoch.Add(3*time.Second), "end_turn", textBlock("What can you do?"))
	next := begin(t, s, "main", append(prefix, user("Who are you?")), epoch.Add(4*time.Second))
	complete(next, epoch.Add(5*time.Second), "end_turn", textBlock("An assistant"))
	if next.turn.session != main.turn.session || next.turn.Number != 2 || next.turn.session.parent != "" {
		t.Fatal("real user instruction forked after a suggestion")
	}
	if helper.turn.session.parent != main.turn.session.id || helper.turn.session.forkTurn != main.turn.ID {
		t.Fatal("helper lost the shared-prefix anchor")
	}
}

func TestSuggestionFirstReservesMainWithoutInventedTurns(t *testing.T) {
	s := New()
	prefix := []any{user("Hello"), assistant(textBlock("Welcome")), user("Who are you?"), assistant(textBlock("An assistant"))}
	helper := begin(t, s, "main", append(prefix, suggestion("Predict next.")), epoch.Add(5*time.Second))
	complete(helper, epoch.Add(6*time.Second), "end_turn")
	visible := s.Snapshot(epoch.Add(7 * time.Second)).Sessions
	if len(visible) != 1 || visible[0].Kind != "suggestion" || visible[0].ParentID == "" {
		t.Fatal("unobserved main work was fabricated or helper became the root")
	}
	// The main request can be observed late, after its response was already in
	// the helper's input prefix. It must claim the reserved main identity.
	main := begin(t, s, "main", prefix[:3], epoch.Add(2*time.Second))
	complete(main, epoch.Add(4*time.Second), "end_turn", textBlock("An assistant"))
	if main.turn.session.parent != "" || helper.turn.session.parent != main.turn.session.id || helper.turn.session.forkTurn != main.turn.ID {
		t.Fatal("arrival order reversed main/helper ancestry")
	}
	if len(s.Snapshot(epoch.Add(8*time.Second)).Sessions) != 2 {
		t.Fatal("late main created an extra lane")
	}
}

func TestSuggestionDuringMainResponseResolvesAnchor(t *testing.T) {
	s := New()
	main := begin(t, s, "main", []any{user("Hello")}, epoch)
	helper := begin(t, s, "main", []any{user("Hello"), assistant(textBlock("Welcome")), suggestion("Predict next.")}, epoch.Add(time.Second))
	complete(helper, epoch.Add(3*time.Second), "end_turn")
	complete(main, epoch.Add(2*time.Second), "end_turn", textBlock("Welcome"))
	if helper.turn.session.parent != main.turn.session.id || helper.turn.session.forkTurn != main.turn.ID {
		t.Fatal("overlapping helper stayed orphaned")
	}
	if len(s.Snapshot(epoch.Add(4*time.Second)).Sessions) != 2 {
		t.Fatal("overlap produced extra sessions")
	}
}

func TestSuggestionRetriesAndToolContinuationsStayPrivate(t *testing.T) {
	s := New()
	main := begin(t, s, "main", []any{user("Work")}, epoch)
	complete(main, epoch.Add(time.Second), "end_turn", textBlock("Done"))
	prefix := []any{user("Work"), assistant(textBlock("Done"))}
	history := append(prefix, suggestion("Predict next."))
	failed := begin(t, s, "main", history, epoch.Add(2*time.Second))
	failed.Finish(epoch.Add(3*time.Second), nil)
	retry := begin(t, s, "main", history, epoch.Add(4*time.Second))
	complete(retry, epoch.Add(5*time.Second), "tool_use", toolBlock("helper-read"))
	history = append(history, assistant(toolBlock("helper-read")), toolResult("helper-read", "ok"))
	last := begin(t, s, "main", history, epoch.Add(6*time.Second))
	complete(last, epoch.Add(7*time.Second), "end_turn", textBlock("Next task"))
	if failed.turn.session != retry.turn.session || retry.turn != last.turn || retry.turn.Attempt != 2 || len(main.turn.session.turns) != 1 {
		t.Fatal("helper retries or tools escaped into main history")
	}
	// A separate one-shot call at a new main checkpoint gets its own child row.
	next := begin(t, s, "main", append(prefix, user("Next")), epoch.Add(8*time.Second))
	complete(next, epoch.Add(9*time.Second), "end_turn", textBlock("Next answer"))
	fresh := begin(t, s, "main", append(append(prefix, user("Next"), assistant(textBlock("Next answer"))), suggestion("Predict again.")), epoch.Add(10*time.Second))
	complete(fresh, epoch.Add(11*time.Second), "end_turn")
	if fresh.turn.session == last.turn.session || fresh.turn.session.parent != main.turn.session.id {
		t.Fatal("different helper invocations shared a mutable lane")
	}
}

func TestSuggestionsAfterCompactionRemainSeparateInvocations(t *testing.T) {
	s := New()
	history := []any{user("This session is being continued from a previous conversation that ran out of context."), user("Current task")}
	main := begin(t, s, "main", history, epoch)
	complete(main, epoch.Add(time.Second), "end_turn", textBlock("Ready"))
	history = append(history, assistant(textBlock("Ready")))
	first := begin(t, s, "main", append(append([]any{}, history...), suggestion("Predict.")), epoch.Add(2*time.Second))
	complete(first, epoch.Add(3*time.Second), "end_turn")
	history = append(history, user("Next task"))
	next := begin(t, s, "main", history, epoch.Add(4*time.Second))
	complete(next, epoch.Add(5*time.Second), "end_turn", textBlock("Done"))
	history = append(history, assistant(textBlock("Done")), suggestion("Predict."))
	second := begin(t, s, "main", history, epoch.Add(6*time.Second))
	complete(second, epoch.Add(7*time.Second), "end_turn")
	if first.turn.session == second.turn.session || next.turn.session != main.turn.session {
		t.Fatal("historical compaction marker merged unrelated helper calls")
	}
}

func TestSuggestionMarkerOnlyClassifiesCurrentClaudeInstruction(t *testing.T) {
	for _, tc := range []struct {
		name     string
		headers  http.Header
		messages []any
		purpose  string
	}{
		{"current", http.Header{"X-Claude-Code-Session-Id": {"id"}}, []any{suggestion("Predict.")}, "suggestion"},
		{"metadata", http.Header{"User-Agent": {"claude-cli/2"}}, []any{suggestion("Predict.")}, ""}, // no stable session identity
		{"quoted", http.Header{"X-Claude-Code-Session-Id": {"id"}}, []any{user("Explain this marker: " + suggestionInstruction)}, ""},
		{"historical", http.Header{"X-Claude-Code-Session-Id": {"id"}}, []any{suggestion("Old helper"), assistant(textBlock("Old result")), user("Real input")}, ""},
		{"tool output", http.Header{"X-Claude-Code-Session-Id": {"id"}}, []any{user("Read the log"), assistant(toolBlock("read")), toolResult("read", suggestionInstruction)}, ""},
		{"other client", http.Header{"Thread-Id": {"id"}}, []any{suggestion("Literal text")}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := parseRequest(raw(object{"model": "m", "messages": tc.messages}), tc.headers, "/v1/messages")
			if err != nil {
				t.Fatal(err)
			}
			if p.purpose != tc.purpose {
				t.Fatalf("purpose=%q want %q", p.purpose, tc.purpose)
			}
		})
	}
}
