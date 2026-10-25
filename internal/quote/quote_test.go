package quote

import "testing"

// TestTransitionTable pins the workflow agreed for Phase 8. If someone
// edits the transitions map, this test says exactly which rule changed.
func TestTransitionTable(t *testing.T) {
	statuses := []string{StatusPending, StatusReviewing, StatusQuoted, StatusAccepted, StatusRejected, StatusCancelled, StatusDeclined}
	allowed := map[string]map[string]bool{
		"review":  {StatusPending: true},
		"quote":   {StatusPending: true, StatusReviewing: true, StatusQuoted: true},
		"decline": {StatusPending: true, StatusReviewing: true},
		"accept":  {StatusQuoted: true},
		"reject":  {StatusQuoted: true},
		"cancel":  {StatusPending: true, StatusReviewing: true, StatusQuoted: true},
	}
	actors := map[string]Actor{
		"review": Company, "quote": Company, "decline": Company,
		"accept": Customer, "reject": Customer, "cancel": Customer,
	}
	if len(transitions) != len(allowed) {
		t.Fatalf("transitions has %d actions, want %d", len(transitions), len(allowed))
	}
	for action, tr := range transitions {
		if tr.actor != actors[action] {
			t.Errorf("%s: wrong actor", action)
		}
		for _, s := range statuses {
			if got := tr.allows(s); got != allowed[action][s] {
				t.Errorf("%s from %s: allowed=%v, want %v", action, s, got, allowed[action][s])
			}
		}
	}
	for _, s := range statuses {
		final := s == StatusAccepted || s == StatusRejected || s == StatusCancelled || s == StatusDeclined
		if IsFinal(s) != final {
			t.Errorf("IsFinal(%s) = %v", s, IsFinal(s))
		}
		// Final statuses allow nothing.
		for action, tr := range transitions {
			if final && tr.allows(s) {
				t.Errorf("%s allowed from final status %s", action, s)
			}
		}
	}
}
