package dispatch

import (
	"testing"
	"time"
)

func TestEventRouter_Match_ExactTypeAndRepo(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "kernel-pr-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/kernel",
			AgentName: "kernel-ci-agent",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	// Test matching PR opened event
	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/kernel",
	}

	matches := er.Match(event)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for PR opened event, got %d", len(matches))
	}
	if matches[0].AgentName != "kernel-pr-agent" {
		t.Fatalf("expected kernel-pr-agent, got %s", matches[0].AgentName)
	}

	// Test matching CI completed event
	event.Type = EventCICompleted
	matches = er.Match(event)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for CI completed event, got %d", len(matches))
	}
	if matches[0].AgentName != "kernel-ci-agent" {
		t.Fatalf("expected kernel-ci-agent, got %s", matches[0].AgentName)
	}
}

func TestEventRouter_Match_GlobPattern(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/*",
			AgentName: "wildcard-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "exact-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	// Test glob pattern matching
	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/cloud",
	}

	matches := er.Match(event)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for glob pattern, got %d", len(matches))
	}
	if matches[0].AgentName != "wildcard-agent" {
		t.Fatalf("expected wildcard-agent, got %s", matches[0].AgentName)
	}

	// Test exact match also works
	event.Repo = "chitinhq/kernel"
	matches = er.Match(event)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for exact repo (glob + exact), got %d", len(matches))
	}
}

func TestEventRouter_Match_EmptyRepoMatch(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventTimer,
			RepoMatch: "", // Empty repo match means match any repo
			AgentName: "timer-agent",
			Priority:  2,
			Cooldown:  3 * time.Hour,
		},
	}

	er := NewEventRouter(rules)

	// Test with repo
	event := Event{
		Type:   EventTimer,
		Source: "cron",
		Repo:   "chitinhq/kernel",
	}

	matches := er.Match(event)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for timer event with repo, got %d", len(matches))
	}

	// Test without repo
	event.Repo = ""
	matches = er.Match(event)
	if len(matches) != 1 {
		t.Fatalf("expected 1 match for timer event without repo, got %d", len(matches))
	}
}

func TestEventRouter_Match_NoMatch(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/*",
			AgentName: "test-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	// Wrong event type
	event := Event{
		Type:   EventCICompleted,
		Source: "github",
		Repo:   "chitinhq/kernel",
	}

	matches := er.Match(event)
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches for wrong event type, got %d", len(matches))
	}

	// Wrong repo
	event.Type = EventPROpened
	event.Repo = "otherorg/repo"
	matches = er.Match(event)
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches for wrong repo, got %d", len(matches))
	}
}

func TestEventRouter_Match_MultipleRulesSameEvent(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/*",
			AgentName: "wildcard-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "kernel-agent",
			Priority:  0, // Higher priority (lower number)
			Cooldown:  2 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "backup-kernel-agent",
			Priority:  2, // Lower priority
			Cooldown:  10 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/kernel",
	}

	matches := er.Match(event)
	if len(matches) != 3 {
		t.Fatalf("expected 3 matches for kernel repo, got %d", len(matches))
	}

	// Verify all expected agents are present
	agents := make(map[string]bool)
	for _, match := range matches {
		agents[match.AgentName] = true
	}

	expectedAgents := []string{"wildcard-agent", "kernel-agent", "backup-kernel-agent"}
	for _, expected := range expectedAgents {
		if !agents[expected] {
			t.Fatalf("expected agent %s in matches", expected)
		}
	}
}

func TestEventRouter_CooldownFor_SingleRule(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "test-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	cooldown := er.CooldownFor("test-agent")
	if cooldown != 5*time.Minute {
		t.Fatalf("expected 5m cooldown, got %v", cooldown)
	}
}

func TestEventRouter_CooldownFor_MultipleRulesSameAgent(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "test-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventCICompleted,
			RepoMatch: "chitinhq/kernel",
			AgentName: "test-agent",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "test-agent",
			Priority:  3,
			Cooldown:  1 * time.Hour,
		},
	}

	er := NewEventRouter(rules)

	cooldown := er.CooldownFor("test-agent")
	if cooldown != 1*time.Hour {
		t.Fatalf("expected 1h cooldown (longest of 5m, 10m, 1h), got %v", cooldown)
	}
}

func TestEventRouter_CooldownFor_AgentNotFound(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "test-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	cooldown := er.CooldownFor("non-existent-agent")
	if cooldown != 0 {
		t.Fatalf("expected 0 cooldown for non-existent agent, got %v", cooldown)
	}
}

func TestEventRouter_CooldownFor_ZeroCooldown(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventManual,
			RepoMatch: "",
			AgentName: "manual-agent",
			Priority:  0,
			Cooldown:  0, // No cooldown
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel",
			AgentName: "manual-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	cooldown := er.CooldownFor("manual-agent")
	if cooldown != 5*time.Minute {
		t.Fatalf("expected 5m cooldown (longest of 0 and 5m), got %v", cooldown)
	}
}

func TestEventRouter_CooldownFor_WildcardAgent(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventManual,
			RepoMatch: "",
			AgentName: "*", // Wildcard agent
			Priority:  0,
			Cooldown:  2 * time.Minute,
		},
		{
			EventType: EventSlackAction,
			RepoMatch: "",
			AgentName: "*", // Same wildcard
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	cooldown := er.CooldownFor("*")
	if cooldown != 5*time.Minute {
		t.Fatalf("expected 5m cooldown for wildcard agent, got %v", cooldown)
	}
}

func TestEventRouter_Match_ComplexGlobPatterns(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/*-agent",
			AgentName: "dash-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "chitinhq/kernel-*",
			AgentName: "kernel-prefix-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventPROpened,
			RepoMatch: "*/kernel",
			AgentName: "any-org-kernel-agent",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	// Test dash pattern
	event := Event{
		Type:   EventPROpened,
		Source: "github",
		Repo:   "chitinhq/test-agent",
	}
	matches := er.Match(event)
	if len(matches) != 1 || matches[0].AgentName != "dash-agent" {
		t.Fatalf("expected dash-agent to match chitinhq/test-agent")
	}

	// Test kernel prefix pattern
	event.Repo = "chitinhq/kernel-v2"
	matches = er.Match(event)
	if len(matches) != 1 || matches[0].AgentName != "kernel-prefix-agent" {
		t.Fatalf("expected kernel-prefix-agent to match chitinhq/kernel-v2")
	}

	// Test any org pattern
	event.Repo = "otherorg/kernel"
	matches = er.Match(event)
	if len(matches) != 1 || matches[0].AgentName != "any-org-kernel-agent" {
		t.Fatalf("expected any-org-kernel-agent to match otherorg/kernel")
	}
}

func TestEventRouter_Match_EventWithEmptyRepo(t *testing.T) {
	rules := []EventRule{
		{
			EventType: EventTimer,
			RepoMatch: "chitinhq/*",
			AgentName: "repo-specific-timer",
			Priority:  1,
			Cooldown:  5 * time.Minute,
		},
		{
			EventType: EventTimer,
			RepoMatch: "",
			AgentName: "global-timer",
			Priority:  2,
			Cooldown:  10 * time.Minute,
		},
	}

	er := NewEventRouter(rules)

	// Event with empty repo matches all rules (glob check is skipped when event.Repo is empty)
	event := Event{
		Type:   EventTimer,
		Source: "cron",
		Repo:   "", // Empty repo
	}

	matches := er.Match(event)
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for event with empty repo (glob check skipped), got %d", len(matches))
	}
	
	// Both rules should match
	agents := make(map[string]bool)
	for _, match := range matches {
		agents[match.AgentName] = true
	}
	
	if !agents["repo-specific-timer"] {
		t.Error("expected repo-specific-timer to match (glob check skipped when event.Repo is empty)")
	}
	if !agents["global-timer"] {
		t.Error("expected global-timer to match")
	}
}

func TestEventRouter_DefaultRules(t *testing.T) {
	rules := DefaultRules()
	if len(rules) == 0 {
		t.Fatal("expected default rules to be non-empty")
	}

	// Verify some expected rules exist
	hasPROpenedKernel := false
	hasTimerKernelSR := false
	hasManualWildcard := false

	for _, rule := range rules {
		if rule.EventType == EventPROpened && rule.RepoMatch == "chitinhq/kernel" && rule.AgentName == "workspace-pr-review-agent" {
			hasPROpenedKernel = true
		}
		if rule.EventType == EventTimer && rule.AgentName == "kernel-sr" {
			hasTimerKernelSR = true
		}
		if rule.EventType == EventManual && rule.AgentName == "*" {
			hasManualWildcard = true
		}
	}

	if !hasPROpenedKernel {
		t.Error("expected default rules to include PR opened for chitinhq/kernel")
	}
	if !hasTimerKernelSR {
		t.Error("expected default rules to include timer for kernel-sr")
	}
	if !hasManualWildcard {
		t.Error("expected default rules to include manual wildcard")
	}
}