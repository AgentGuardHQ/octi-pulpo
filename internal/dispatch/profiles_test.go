package dispatch

import (
	"testing"
	"time"
)

func TestProfileStore_RecordAndGet(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Record a productive run
	err := ps.RecordRun(ctx, "test-sr", RunResult{
		ExitCode:   0,
		Duration:   120.5,
		HadCommits: true,
		Timestamp:  "2026-03-29T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("record run: %v", err)
	}

	profile, err := ps.GetProfile(ctx, "test-sr")
	if err != nil {
		t.Fatalf("get profile: %v", err)
	}

	if len(profile.RecentResults) != 1 {
		t.Fatalf("expected 1 result, got %d", len(profile.RecentResults))
	}
	if profile.AvgDuration != 120.5 {
		t.Fatalf("expected avg duration 120.5, got %.1f", profile.AvgDuration)
	}
	if profile.FailRate != 0 {
		t.Fatalf("expected 0 fail rate, got %.2f", profile.FailRate)
	}
}

func TestProfileStore_KeepsLast10(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	for i := 0; i < 15; i++ {
		ps.RecordRun(ctx, "prolific-agent", RunResult{
			ExitCode: 0,
			Duration: float64(i * 10),
		})
	}

	profile, _ := ps.GetProfile(ctx, "prolific-agent")
	if len(profile.RecentResults) != 10 {
		t.Fatalf("expected 10 results (capped), got %d", len(profile.RecentResults))
	}

	// First result should be the 6th run (index 5), not the first
	if profile.RecentResults[0].Duration != 50 {
		t.Fatalf("expected first result duration=50, got %.0f", profile.RecentResults[0].Duration)
	}
}

func TestProfileStore_ConsecutiveIdles(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Record 3 idle runs
	for i := 0; i < 3; i++ {
		ps.RecordRun(ctx, "idle-agent", RunResult{
			ExitCode:   0,
			Duration:   5.0,
			HadCommits: false,
		})
	}

	profile, _ := ps.GetProfile(ctx, "idle-agent")
	if profile.ConsecutiveIdles != 3 {
		t.Fatalf("expected 3 consecutive idles, got %d", profile.ConsecutiveIdles)
	}

	// Record a productive run — resets counter
	ps.RecordRun(ctx, "idle-agent", RunResult{
		ExitCode:   0,
		Duration:   60.0,
		HadCommits: true,
	})

	profile, _ = ps.GetProfile(ctx, "idle-agent")
	if profile.ConsecutiveIdles != 0 {
		t.Fatalf("expected 0 consecutive idles after productive run, got %d", profile.ConsecutiveIdles)
	}
}

func TestAdaptiveCooldown_Productive(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	ps.RecordRun(ctx, "productive-agent", RunResult{
		ExitCode:   0,
		Duration:   120.0,
		HadCommits: true,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "productive-agent")
	if cooldown != 5*time.Minute {
		t.Fatalf("expected 5m for productive agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_Idle(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 10 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	ps.RecordRun(ctx, "idle-agent", RunResult{
		ExitCode:   0,
		Duration:   5.0,
		HadCommits: false,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "idle-agent")
	// Should double from 10m -> 20m
	if cooldown != 20*time.Minute {
		t.Fatalf("expected 20m for idle agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_Failing(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 15 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	// Record mostly failures
	for i := 0; i < 4; i++ {
		ps.RecordRun(ctx, "fail-agent", RunResult{
			ExitCode: 1,
			Duration: 30.0,
		})
	}
	ps.RecordRun(ctx, "fail-agent", RunResult{
		ExitCode: 0,
		Duration: 30.0,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "fail-agent")
	// 80% fail rate > 50%, should double from 15m -> 30m
	if cooldown != 30*time.Minute {
		t.Fatalf("expected 30m for failing agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_NoHistory(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// kernel-sr has a 3h static cooldown
	cooldown := ps.AdaptiveCooldown(ctx, "kernel-sr")
	if cooldown != 3*time.Hour {
		t.Fatalf("expected 3h static fallback for kernel-sr, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_IdleMaxCap(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 4 * time.Hour }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	ps.RecordRun(ctx, "very-idle", RunResult{
		ExitCode:   0,
		Duration:   2.0,
		HadCommits: false,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "very-idle")
	// Doubling 4h would be 8h, but cap is 6h
	if cooldown != 6*time.Hour {
		t.Fatalf("expected 6h cap for idle agent, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_FailMaxCap(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 90 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)

	// Use alternating fail/success so fail rate stays high (80%) but
	// ConsecutiveFails never reaches 3 (no triage flag).
	// Pattern: fail, success, fail, success, fail → ConsecutiveFails=1
	runs := []RunResult{
		{ExitCode: 1, Duration: 30.0},
		{ExitCode: 0, Duration: 30.0},
		{ExitCode: 1, Duration: 30.0},
		{ExitCode: 0, Duration: 30.0},
		{ExitCode: 1, Duration: 30.0},
	}
	for _, r := range runs {
		ps.RecordRun(ctx, "fail-cap-agent", r)
	}

	cooldown := ps.AdaptiveCooldown(ctx, "fail-cap-agent")
	// fail rate=60% > 50%, ConsecutiveFails=1 (no triage),
	// doubling 90m → 180m → capped at 2h
	if cooldown != 2*time.Hour {
		t.Fatalf("expected 2h cap for failing agent, got %s", cooldown)
	}
}

func TestProfileStore_ConsecutiveFails(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Three failures in a row
	for i := 0; i < 3; i++ {
		ps.RecordRun(ctx, "flaky-agent", RunResult{
			ExitCode: 1,
			Duration: 30.0,
		})
	}

	profile, _ := ps.GetProfile(ctx, "flaky-agent")
	if profile.ConsecutiveFails != 3 {
		t.Fatalf("expected ConsecutiveFails=3, got %d", profile.ConsecutiveFails)
	}
	if !profile.TriageFlag {
		t.Fatal("expected TriageFlag=true after 3 consecutive failures")
	}

	// A success clears both counters
	ps.RecordRun(ctx, "flaky-agent", RunResult{
		ExitCode:   0,
		Duration:   60.0,
		HadCommits: true,
	})

	profile, _ = ps.GetProfile(ctx, "flaky-agent")
	if profile.ConsecutiveFails != 0 {
		t.Fatalf("expected ConsecutiveFails=0 after success, got %d", profile.ConsecutiveFails)
	}
	if profile.TriageFlag {
		t.Fatal("expected TriageFlag=false after successful run")
	}
}

func TestProfileStore_TriageFlagAtThreshold(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Two failures should NOT set triage flag
	for i := 0; i < 2; i++ {
		ps.RecordRun(ctx, "borderline-agent", RunResult{ExitCode: 1, Duration: 30.0})
	}
	profile, _ := ps.GetProfile(ctx, "borderline-agent")
	if profile.TriageFlag {
		t.Fatal("expected TriageFlag=false after only 2 consecutive failures")
	}

	// Third failure sets it
	ps.RecordRun(ctx, "borderline-agent", RunResult{ExitCode: 1, Duration: 30.0})
	profile, _ = ps.GetProfile(ctx, "borderline-agent")
	if !profile.TriageFlag {
		t.Fatal("expected TriageFlag=true after 3rd consecutive failure")
	}
}

func TestAdaptiveCooldown_TriageFlag(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	for i := 0; i < 3; i++ {
		ps.RecordRun(ctx, "stuck-agent", RunResult{ExitCode: 1, Duration: 30.0})
	}

	cooldown := ps.AdaptiveCooldown(ctx, "stuck-agent")
	if cooldown != 12*time.Hour {
		t.Fatalf("expected 12h triage cooldown, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_BudgetTightIdle(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 10 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)
	// Budget at 20% health → tight
	ps.SetBudgetHealthFn(func() float64 { return 0.2 })

	ps.RecordRun(ctx, "budget-idle", RunResult{
		ExitCode:   0,
		Duration:   5.0,
		HadCommits: false,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "budget-idle")
	// Budget tight multiplier: 3× instead of 2× → 30m
	if cooldown != 30*time.Minute {
		t.Fatalf("expected 30m (3× idle under tight budget), got %s", cooldown)
	}
}

func TestAdaptiveCooldown_BudgetHealthyHotStreak(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)
	// Budget at 90% health → healthy
	ps.SetBudgetHealthFn(func() float64 { return 0.9 })

	ps.RecordRun(ctx, "hot-agent", RunResult{
		ExitCode:   0,
		Duration:   120.0,
		HadCommits: true,
	})

	cooldown := ps.AdaptiveCooldown(ctx, "hot-agent")
	// Hot streak + healthy budget → 2.5 min
	if cooldown != 150*time.Second {
		t.Fatalf("expected 150s for hot streak with healthy budget, got %s", cooldown)
	}
}

func TestAdaptiveCooldown_BudgetTightNoHistory(t *testing.T) {
	d, ctx := testSetup(t)
	static := func(string) time.Duration { return 10 * time.Minute }
	ps := NewProfileStore(d.rdb, d.namespace, static)
	ps.SetBudgetHealthFn(func() float64 { return 0.1 }) // all drivers exhausted

	// No run history — should get 3× static cooldown
	cooldown := ps.AdaptiveCooldown(ctx, "new-agent")
	if cooldown != 30*time.Minute {
		t.Fatalf("expected 30m (3× static under tight budget), got %s", cooldown)
	}
}

func TestClassifyAgent_Tiers(t *testing.T) {
	cases := []struct {
		name     string
		profile  AgentProfile
		wantTier LeaderboardTier
	}{
		{
			name: "MVP — high success, high commit rate",
			profile: AgentProfile{
				Name:          "mvp-agent",
				FailRate:      0.05,
				AvgCommits:    0.80,
				AvgDuration:   90.0,
				RecentResults: makeResults(20, 1, 1.0, 0.80), // 20 runs, 95% success, 80% have commits
			},
			wantTier: TierMVP,
		},
		{
			name: "Reliable — decent success, some commits",
			profile: AgentProfile{
				Name:          "solid-agent",
				FailRate:      0.20,
				AvgCommits:    0.40,
				AvgDuration:   60.0,
				RecentResults: makeResults(10, 0, 0.80, 0.40),
			},
			wantTier: TierReliable,
		},
		{
			name: "Underperforming — low success rate",
			profile: AgentProfile{
				Name:          "flaky-agent",
				FailRate:      0.60,
				AvgCommits:    0.20,
				AvgDuration:   30.0,
				RecentResults: makeResults(10, 0, 0.40, 0.20),
			},
			wantTier: TierUnderperforming,
		},
		{
			name: "Underperforming — high waste",
			profile: AgentProfile{
				Name:          "waste-agent",
				FailRate:      0.10,
				AvgCommits:    0.10,
				AvgDuration:   4.0,
				RecentResults: makeWasteResults(10, 0.70), // 70% short no-op runs
			},
			wantTier: TierUnderperforming,
		},
		{
			name: "Firing Line — very high fail rate",
			profile: AgentProfile{
				Name:          "broken-agent",
				FailRate:      0.80,
				AvgCommits:    0.05,
				AvgDuration:   10.0,
				RecentResults: makeResults(10, 0, 0.20, 0.05),
			},
			wantTier: TierFiringLine,
		},
		{
			name: "Firing Line — extreme waste + many idles",
			profile: AgentProfile{
				Name:             "dead-agent",
				FailRate:         0.10,
				AvgCommits:       0.05,
				AvgDuration:      3.0,
				ConsecutiveIdles: 6,
				RecentResults:    makeWasteResults(10, 0.90),
			},
			wantTier: TierFiringLine,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			entry := classifyAgent(tc.profile)
			if entry.Tier != tc.wantTier {
				t.Errorf("agent %q: want tier %q, got %q (successRate=%.2f, commitRate=%.2f, wastePercent=%.1f)",
					tc.profile.Name, tc.wantTier, entry.Tier,
					entry.SuccessRate, entry.CommitRate, entry.WastePercent)
			}
		})
	}
}

func TestLeaderboard_Integration(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, d.events.CooldownFor)

	// Seed a few agents with distinct performance profiles
	recordRuns := func(agent string, runs []RunResult) {
		for _, r := range runs {
			if err := ps.RecordRun(ctx, agent, r); err != nil {
				t.Fatalf("record run for %s: %v", agent, err)
			}
		}
	}

	// MVP: 90% success, 70% commit rate
	recordRuns("mvp-agent", []RunResult{
		{ExitCode: 0, Duration: 90, HadCommits: true},
		{ExitCode: 0, Duration: 80, HadCommits: true},
		{ExitCode: 0, Duration: 70, HadCommits: true},
		{ExitCode: 1, Duration: 60, HadCommits: false},
		{ExitCode: 0, Duration: 100, HadCommits: true},
		{ExitCode: 0, Duration: 90, HadCommits: true},
		{ExitCode: 0, Duration: 85, HadCommits: true},
		{ExitCode: 0, Duration: 75, HadCommits: false},
		{ExitCode: 0, Duration: 65, HadCommits: true},
		{ExitCode: 0, Duration: 95, HadCommits: true},
	})

	// Firing line: 80% fail
	recordRuns("broken-agent", []RunResult{
		{ExitCode: 1, Duration: 30, HadCommits: false},
		{ExitCode: 1, Duration: 25, HadCommits: false},
		{ExitCode: 1, Duration: 20, HadCommits: false},
		{ExitCode: 0, Duration: 60, HadCommits: true},
		{ExitCode: 1, Duration: 15, HadCommits: false},
	})

	lb, err := ps.Leaderboard(ctx)
	if err != nil {
		t.Fatalf("leaderboard: %v", err)
	}

	if lb.TotalAgents != 2 {
		t.Fatalf("expected 2 agents, got %d", lb.TotalAgents)
	}

	if len(lb.MVPs) == 0 {
		t.Error("expected mvp-agent in MVPs tier")
	} else if lb.MVPs[0].Agent != "mvp-agent" {
		t.Errorf("expected mvp-agent as MVP, got %q", lb.MVPs[0].Agent)
	}

	if len(lb.FiringLine) == 0 {
		t.Error("expected broken-agent in FiringLine tier")
	} else if lb.FiringLine[0].Agent != "broken-agent" {
		t.Errorf("expected broken-agent as FiringLine, got %q", lb.FiringLine[0].Agent)
	}
}

// makeResults builds a slice of RunResults with the specified success and commit rates.
func makeResults(n, startExitCode int, successRate, commitRate float64) []RunResult {
	results := make([]RunResult, n)
	successCount := int(float64(n) * successRate)
	commitCount := int(float64(n) * commitRate)
	for i := range results {
		results[i] = RunResult{
			Duration:   60.0,
			ExitCode:   1,
			HadCommits: i < commitCount,
		}
		if i < successCount {
			results[i].ExitCode = 0
		}
		_ = startExitCode // unused, kept for signature clarity
	}
	return results
}

// makeWasteResults builds a slice where wasteRate fraction are short no-op runs.
func makeWasteResults(n int, wasteRate float64) []RunResult {
	results := make([]RunResult, n)
	wasteCount := int(float64(n) * wasteRate)
	for i := range results {
		if i < wasteCount {
			results[i] = RunResult{Duration: 3.0, ExitCode: 0, HadCommits: false}
		} else {
			results[i] = RunResult{Duration: 90.0, ExitCode: 0, HadCommits: true}
		}
	}
	return results
}

// testAdaptiveCooldownForDispatch verifies that the dispatcher integration point works.
func TestAdaptiveCooldown_DefaultFallback(t *testing.T) {
	d, ctx := testSetup(t)
	ps := NewProfileStore(d.rdb, d.namespace, func(string) time.Duration { return 0 })

	// Agent with no static cooldown and no history
	cooldown := ps.AdaptiveCooldown(ctx, "unknown-agent")
	// Should get static fallback (which is 0 here), so the function returns 0
	if cooldown != 0 {
		t.Fatalf("expected 0 for unknown agent with no static, got %s", cooldown)
	}
}
