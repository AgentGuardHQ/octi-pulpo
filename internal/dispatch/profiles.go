package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// AgentProfile tracks an agent's recent execution history for adaptive cooldown tuning.
type AgentProfile struct {
	Name             string        `json:"name"`
	RecentResults    []RunResult   `json:"recent_results"`   // last 10
	AvgDuration      float64       `json:"avg_duration_s"`
	AvgCommits       float64       `json:"avg_commits"`
	FailRate         float64       `json:"fail_rate"`
	CurrentCooldown  time.Duration `json:"current_cooldown"`
	ConsecutiveIdles int           `json:"consecutive_idles"`
	ConsecutiveFails int           `json:"consecutive_fails"`
	// TriageFlag is set when the agent accumulates 3+ consecutive failures and
	// needs human review before being dispatched aggressively again.
	TriageFlag bool `json:"triage_flag,omitempty"`
}

// RunResult is a single agent execution record.
type RunResult struct {
	ExitCode   int     `json:"exit_code"`
	Duration   float64 `json:"duration_s"`
	HadCommits bool    `json:"had_commits"`
	Timestamp  string  `json:"timestamp"`
}

// ProfileStore manages agent execution profiles in Redis.
type ProfileStore struct {
	rdb            *redis.Client
	namespace      string
	staticCooldown func(agent string) time.Duration
	// budgetHealthFn returns the fraction of drivers that are healthy (0.0–1.0).
	// 1.0 = all drivers CLOSED; 0.0 = all drivers OPEN (budget exhausted).
	// Optional — if nil, budget signal is ignored.
	budgetHealthFn func() float64
}

// NewProfileStore creates a profile store.
func NewProfileStore(rdb *redis.Client, namespace string, staticCooldown func(string) time.Duration) *ProfileStore {
	return &ProfileStore{
		rdb:            rdb,
		namespace:      namespace,
		staticCooldown: staticCooldown,
	}
}

// SetBudgetHealthFn wires a live driver-health signal into adaptive cooldown.
// fn should return the fraction of drivers with CLOSED circuits (0.0–1.0).
// The dispatcher in main.go provides this via router.HealthReport().
func (ps *ProfileStore) SetBudgetHealthFn(fn func() float64) {
	ps.budgetHealthFn = fn
}

// RecordRun appends a run result to the agent's profile, keeping the last 10.
func (ps *ProfileStore) RecordRun(ctx context.Context, agent string, result RunResult) error {
	key := ps.profileKey(agent)

	profile, _ := ps.GetProfile(ctx, agent)
	profile.Name = agent
	profile.RecentResults = append(profile.RecentResults, result)

	// Keep last 10
	if len(profile.RecentResults) > 10 {
		profile.RecentResults = profile.RecentResults[len(profile.RecentResults)-10:]
	}

	// Recompute aggregates
	ps.recompute(&profile)

	// Track consecutive idles (short runs with no output)
	if result.Duration < 10 && !result.HadCommits {
		profile.ConsecutiveIdles++
	} else {
		profile.ConsecutiveIdles = 0
	}

	// Track consecutive failures; set triage flag at threshold
	if result.ExitCode != 0 {
		profile.ConsecutiveFails++
		if profile.ConsecutiveFails >= 3 {
			profile.TriageFlag = true
		}
	} else {
		profile.ConsecutiveFails = 0
		profile.TriageFlag = false
	}

	data, err := json.Marshal(profile)
	if err != nil {
		return err
	}
	return ps.rdb.Set(ctx, key, data, 0).Err()
}

// GetProfile reads an agent's execution profile from Redis.
func (ps *ProfileStore) GetProfile(ctx context.Context, agent string) (AgentProfile, error) {
	key := ps.profileKey(agent)
	raw, err := ps.rdb.Get(ctx, key).Result()
	if err != nil {
		return AgentProfile{Name: agent}, nil // not found is ok
	}

	var profile AgentProfile
	if err := json.Unmarshal([]byte(raw), &profile); err != nil {
		return AgentProfile{Name: agent}, fmt.Errorf("parse profile for %s: %w", agent, err)
	}
	return profile, nil
}

// AdaptiveCooldown computes the optimal cooldown for an agent based on recent performance
// and global driver health.
//
// Rules (checked in order):
//  1. Triage: 3+ consecutive failures → 12h (needs human review)
//  2. Productive (commits > 0, duration > 30s) → 5 min, possibly 2.5 min if budget healthy
//  3. Idle (<10s, 0 commits) → double current, max 6h; if budget tight → triple, max 6h
//  4. Failing (>50% fail rate) → double current, max 2h
//  5. Budget tight (<30% healthy drivers) → 3× static cooldown
//  6. Default → static cooldown from event rules
func (ps *ProfileStore) AdaptiveCooldown(ctx context.Context, agent string) time.Duration {
	profile, err := ps.GetProfile(ctx, agent)
	if err != nil || len(profile.RecentResults) == 0 {
		// No history: apply budget multiplier to static cooldown if budget is tight
		return ps.applyBudgetMultiplier(ps.staticCooldown(agent))
	}

	// 1. Triage: agent needs human review — back off hard
	if profile.TriageFlag {
		return 12 * time.Hour
	}

	// Current cooldown baseline (for multiplier calculations)
	current := profile.CurrentCooldown
	if current == 0 {
		current = ps.staticCooldown(agent)
	}
	if current == 0 {
		current = 10 * time.Minute // absolute fallback
	}

	// Look at the most recent result for immediate signals
	latest := profile.RecentResults[len(profile.RecentResults)-1]

	// 2. Productive: commits and meaningful duration
	if latest.HadCommits && latest.Duration > 30 {
		d := 5 * time.Minute
		// Healthy budget (only when health fn configured): reward hot streaks
		if ps.budgetHealthFn != nil && ps.budgetHealth() > 0.8 {
			d = 150 * time.Second // ~2.5 min
		}
		return d
	}

	// 3. Idle: short runs with no output
	if latest.Duration < 10 && !latest.HadCommits {
		multiplier := time.Duration(2)
		if ps.budgetHealthFn != nil && ps.budgetHealth() < 0.3 {
			multiplier = 3 // conserve budget when drivers are stressed
		}
		result := current * multiplier
		if result > 6*time.Hour {
			result = 6 * time.Hour
		}
		return result
	}

	// 4. Failing: high failure rate
	if profile.FailRate > 0.5 {
		doubled := current * 2
		if doubled > 2*time.Hour {
			doubled = 2 * time.Hour
		}
		return doubled
	}

	// 5. Default: static cooldown, adjusted for budget health
	return ps.applyBudgetMultiplier(ps.staticCooldown(agent))
}

// budgetHealth returns the fraction of healthy drivers via budgetHealthFn.
// Returns 1.0 (fully healthy) when no health function is configured.
func (ps *ProfileStore) budgetHealth() float64 {
	if ps.budgetHealthFn == nil {
		return 1.0
	}
	return ps.budgetHealthFn()
}

// applyBudgetMultiplier scales a cooldown based on driver health.
// Budget tight (<30% healthy): 3× cooldown. Normal: unchanged.
// No-ops when budgetHealthFn is not configured.
func (ps *ProfileStore) applyBudgetMultiplier(d time.Duration) time.Duration {
	if ps.budgetHealthFn != nil && ps.budgetHealth() < 0.3 {
		return d * 3
	}
	return d
}

// recompute recalculates aggregate metrics from recent results.
func (ps *ProfileStore) recompute(p *AgentProfile) {
	if len(p.RecentResults) == 0 {
		return
	}

	var totalDuration float64
	var commits int
	var failures int

	for _, r := range p.RecentResults {
		totalDuration += r.Duration
		if r.HadCommits {
			commits++
		}
		if r.ExitCode != 0 {
			failures++
		}
	}

	n := float64(len(p.RecentResults))
	p.AvgDuration = totalDuration / n
	p.AvgCommits = float64(commits) / n
	p.FailRate = float64(failures) / n
}

func (ps *ProfileStore) profileKey(agent string) string {
	return ps.namespace + ":profile:" + agent
}

// --- Leaderboard ---

// LeaderboardTier classifies an agent's performance level.
type LeaderboardTier string

const (
	TierMVP             LeaderboardTier = "mvp"
	TierReliable        LeaderboardTier = "reliable"
	TierUnderperforming LeaderboardTier = "underperforming"
	TierFiringLine      LeaderboardTier = "firing_line"
)

// LeaderboardEntry holds ranked stats for a single agent.
type LeaderboardEntry struct {
	Agent         string          `json:"agent"`
	Tier          LeaderboardTier `json:"tier"`
	TierEmoji     string          `json:"tier_emoji"`
	SuccessRate   float64         `json:"success_rate"`    // fraction 0–1
	CommitRate    float64         `json:"commit_rate"`     // fraction of runs with commits
	WastePercent  float64         `json:"waste_percent"`   // % of runs that are short no-ops
	AvgDurationS  float64         `json:"avg_duration_s"`
	RunCount      int             `json:"run_count"`
	TriageFlag    bool            `json:"triage_flag,omitempty"`
	ConsecFails   int             `json:"consec_fails,omitempty"`
}

// LeaderboardResult is the full ranked output grouped by tier.
type LeaderboardResult struct {
	Timestamp       string             `json:"timestamp"`
	MVPs            []LeaderboardEntry `json:"mvp"`
	Reliable        []LeaderboardEntry `json:"reliable"`
	Underperforming []LeaderboardEntry `json:"underperforming"`
	FiringLine      []LeaderboardEntry `json:"firing_line"`
	TotalAgents     int                `json:"total_agents"`
}

// Leaderboard scans all agent profiles from Redis and returns a ranked result.
func (ps *ProfileStore) Leaderboard(ctx context.Context) (LeaderboardResult, error) {
	var result LeaderboardResult
	result.Timestamp = time.Now().UTC().Format(time.RFC3339)

	// Scan all profile keys in this namespace
	pattern := ps.namespace + ":profile:*"
	var cursor uint64
	var keys []string
	for {
		batch, next, err := ps.rdb.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return result, fmt.Errorf("scan profiles: %w", err)
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}

	prefix := ps.namespace + ":profile:"
	for _, key := range keys {
		agentName := strings.TrimPrefix(key, prefix)
		profile, err := ps.GetProfile(ctx, agentName)
		if err != nil || len(profile.RecentResults) == 0 {
			continue
		}

		entry := classifyAgent(profile)
		switch entry.Tier {
		case TierMVP:
			result.MVPs = append(result.MVPs, entry)
		case TierReliable:
			result.Reliable = append(result.Reliable, entry)
		case TierUnderperforming:
			result.Underperforming = append(result.Underperforming, entry)
		case TierFiringLine:
			result.FiringLine = append(result.FiringLine, entry)
		}
	}

	// Sort each tier by success rate desc, then commit rate desc
	sortEntries := func(entries []LeaderboardEntry) {
		sort.Slice(entries, func(i, j int) bool {
			if entries[i].SuccessRate != entries[j].SuccessRate {
				return entries[i].SuccessRate > entries[j].SuccessRate
			}
			return entries[i].CommitRate > entries[j].CommitRate
		})
	}
	sortEntries(result.MVPs)
	sortEntries(result.Reliable)
	sortEntries(result.Underperforming)
	sortEntries(result.FiringLine)

	result.TotalAgents = len(result.MVPs) + len(result.Reliable) +
		len(result.Underperforming) + len(result.FiringLine)
	return result, nil
}

// classifyAgent builds a LeaderboardEntry and assigns a tier based on profile metrics.
//
// Tier rules (evaluated in order — first match wins):
//
//	FiringLine:      fail rate ≥ 70% or (waste ≥ 80% and ≥ 5 consecutive idles)
//	MVP:             success rate ≥ 80% and commit rate ≥ 60%
//	Reliable:        success rate ≥ 70% and commit rate ≥ 30%
//	Underperforming: success rate < 50% or waste ≥ 60%
//	default:         Reliable
func classifyAgent(p AgentProfile) LeaderboardEntry {
	var waste int
	for _, r := range p.RecentResults {
		if r.Duration < 10 && !r.HadCommits {
			waste++
		}
	}
	n := len(p.RecentResults)
	wastePercent := float64(waste) / float64(n) * 100
	successRate := 1.0 - p.FailRate

	entry := LeaderboardEntry{
		Agent:        p.Name,
		SuccessRate:  successRate,
		CommitRate:   p.AvgCommits,
		WastePercent: wastePercent,
		AvgDurationS: p.AvgDuration,
		RunCount:     n,
		TriageFlag:   p.TriageFlag,
		ConsecFails:  p.ConsecutiveFails,
	}

	switch {
	case p.FailRate >= 0.70 || (wastePercent >= 80 && p.ConsecutiveIdles >= 5):
		entry.Tier = TierFiringLine
		entry.TierEmoji = "🔴"
	case successRate >= 0.80 && p.AvgCommits >= 0.60:
		entry.Tier = TierMVP
		entry.TierEmoji = "🏆"
	case successRate < 0.50 || wastePercent >= 60:
		entry.Tier = TierUnderperforming
		entry.TierEmoji = "⚠️"
	case successRate >= 0.70 && p.AvgCommits >= 0.30:
		entry.Tier = TierReliable
		entry.TierEmoji = "✅"
	default:
		entry.Tier = TierReliable
		entry.TierEmoji = "✅"
	}

	return entry
}
