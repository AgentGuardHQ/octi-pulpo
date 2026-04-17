package sprint

import "testing"

// TestDispatchLabelStatus_ClosedSetSuppressesClaim is the guard proof for the
// turing slice of /go session 2026-04-17-0006. Before the fix,
// dispatchLabelStatus unconditionally promoted any "agent:claimed" label to
// status="claimed" on every Sync — including issues GitHub had already closed,
// whose labels are zombie locks. This test pins that a closed issue carrying a
// stale "agent:claimed" label resolves to "" (no status override), so the
// caller's downstream SyncClosed / tombstoneFromOpenSet path can mark the item
// done instead of re-animating the zombie claim.
func TestDispatchLabelStatus_ClosedSetSuppressesClaim(t *testing.T) {
	closed := map[int]bool{62: true}

	// Zombie: issue #62 is in the closed set AND carries agent:claimed.
	if got := dispatchLabelStatus(62, []string{"agent:claimed"}, closed); got != "" {
		t.Errorf("closed issue with stale agent:claimed: got %q, want \"\" (no override, so SyncClosed wins)", got)
	}

	// Open counterpart: issue #58 is NOT in the closed set — claim still wins.
	if got := dispatchLabelStatus(58, []string{"agent:claimed"}, closed); got != "claimed" {
		t.Errorf("open issue with agent:claimed: got %q, want \"claimed\"", got)
	}

	// Empty closed set = pre-fix behavior preserved.
	if got := dispatchLabelStatus(58, []string{"agent:claimed"}, nil); got != "claimed" {
		t.Errorf("nil closedSet with agent:claimed: got %q, want \"claimed\" (backward-compatible)", got)
	}

	// Terminal label on a closed issue still resolves — only the claim-lock is
	// suppressed, since it's the re-animation vector.
	if got := dispatchLabelStatus(62, []string{"agent:done"}, closed); got != "done" {
		t.Errorf("closed issue with agent:done: got %q, want \"done\" (terminal wins)", got)
	}

	// Blocked label is not suppressed either — only agent:claimed is the zombie.
	if got := dispatchLabelStatus(62, []string{"agent:blocked"}, closed); got != "blocked" {
		t.Errorf("closed issue with agent:blocked: got %q, want \"blocked\"", got)
	}
}

// TestDispatchLabelStatus_PriorityIndependentOfLabelOrder pins the fix for
// Copilot's review note on PR #276: GitHub does not guarantee label ordering
// on an issue, so dispatchLabelStatus must pick by priority (terminal > review
// > blocked > claimed) rather than by first-match in the slice. Before the
// priority-set fix, [agent:claimed, agent:done] resolved to "claimed" and
// shadowed the terminal state.
func TestDispatchLabelStatus_PriorityIndependentOfLabelOrder(t *testing.T) {
	cases := []struct {
		name   string
		labels []string
		want   string
	}{
		{"done-before-claimed", []string{"agent:done", "agent:claimed"}, "done"},
		{"claimed-before-done", []string{"agent:claimed", "agent:done"}, "done"},
		{"review-beats-claimed-order1", []string{"agent:review", "agent:claimed"}, "pr_open"},
		{"review-beats-claimed-order2", []string{"agent:claimed", "agent:review"}, "pr_open"},
		{"blocked-beats-claimed-order1", []string{"agent:blocked", "agent:claimed"}, "blocked"},
		{"blocked-beats-claimed-order2", []string{"agent:claimed", "agent:blocked"}, "blocked"},
		{"done-beats-blocked", []string{"agent:blocked", "agent:done"}, "done"},
		{"noise-around-done", []string{"bug", "agent:done", "P1"}, "done"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := dispatchLabelStatus(1, tc.labels, nil); got != tc.want {
				t.Errorf("labels=%v: got %q, want %q", tc.labels, got, tc.want)
			}
		})
	}
}

// TestDispatchLabelStatus_NoDispatchLabel confirms the empty-string return is
// preserved when no dispatch label is present, regardless of closedSet state.
func TestDispatchLabelStatus_NoDispatchLabel(t *testing.T) {
	if got := dispatchLabelStatus(1, []string{"bug", "P1"}, nil); got != "" {
		t.Errorf("no dispatch label, nil closedSet: got %q, want \"\"", got)
	}
	if got := dispatchLabelStatus(1, []string{"bug", "P1"}, map[int]bool{1: true}); got != "" {
		t.Errorf("no dispatch label, in closedSet: got %q, want \"\"", got)
	}
}
