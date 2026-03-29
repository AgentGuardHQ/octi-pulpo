package routing

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// CostTier represents the cost category of a driver.
type CostTier string

const (
	TierLocal        CostTier = "local"
	TierSubscription CostTier = "subscription"
	TierCLI          CostTier = "cli"
	TierAPI          CostTier = "api"
)

// tierOrder defines the cost cascade: cheapest first.
var tierOrder = []CostTier{TierLocal, TierSubscription, TierCLI, TierAPI}

// driverTiers maps each known driver to its cost tier.
var driverTiers = map[string]CostTier{
	// Local ($0)
	"ollama":   TierLocal,
	"nemotron": TierLocal,
	// Subscription (browser-based, already-paying seat)
	"openclaw": TierSubscription,
	// CLI (already-paying seat: coding, PRs, commits)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token: burst capacity, programmatic access)
	"anthropic-api": TierAPI,
	"openai-api":    TierAPI,
}

// DriverHealth represents the runtime health of a single driver.
type DriverHealth struct {
	Name         string `json:"name"`
	CircuitState string `json:"circuit_state"` // CLOSED, OPEN, HALF
	Failures     int    `json:"failures"`
	LastFailure  string `json:"last_failure"`
	LastSuccess  string `json:"last_success"`
}

// RouteDecision is the output of the routing engine.
type RouteDecision struct {
	Driver     string   `json:"driver"`
	Tier       string   `json:"tier"`
	Confidence float64  `json:"confidence"`
	Reason     string   `json:"reason"`
	Fallbacks  []string `json:"fallbacks"`
	Skip       bool     `json:"skip"`
}

// Router makes budget-aware driver routing decisions.
type Router struct {
	healthDir string
}

// NewRouter creates a Router that reads driver health from the given directory.
// If healthDir is empty, it defaults to ~/.agentguard/driver-health/.
func NewRouter(healthDir string) *Router {
	if healthDir == "" {
		home, _ := os.UserHomeDir()
		healthDir = filepath.Join(home, ".agentguard", "driver-health")
	}
	return &Router{healthDir: healthDir}
}

// Recommend returns the cheapest healthy driver for the given task.
//
// Two constraints bound the tier search:
//   - minTier (from taskType affinity): coding tasks won't route to local models
//   - maxTier (from budget): high-cost tiers are excluded when budget is constrained
//
// If minTier > maxTier, returns Skip with an explanatory reason.
// Budget values: "low" (local only), "medium" (up to cli), "high"/"" (all tiers).
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	minTier := taskAffinityTier(taskType)
	maxTier := maxTierForBudget(budget)

	if tierIndex(minTier) > tierIndex(maxTier) {
		return RouteDecision{
			Skip:   true,
			Reason: fmt.Sprintf("task type requires %s tier minimum, but budget allows %s maximum", minTier, maxTier),
		}
	}

	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order: cheapest first, bounded by [minTier, maxTier].
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // below task affinity minimum
		}
		if tierIndex(tier) > tierIndex(maxTier) {
			break
		}
		for name, health := range healthMap {
			if tierFor(name) != tier {
				continue
			}
			if health.CircuitState == "OPEN" {
				continue // skip exhausted drivers
			}

			confidence := 1.0
			if health.CircuitState == "HALF" {
				confidence = 0.5
			}

			if chosen == nil {
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
					Confidence: confidence,
					Reason:     fmt.Sprintf("cheapest healthy driver in allowed range (tier: %s, state: %s)", tier, health.CircuitState),
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}
	}

	if chosen == nil {
		return RouteDecision{
			Skip:   true,
			Reason: "all drivers exhausted — circuit breakers OPEN",
		}
	}

	chosen.Fallbacks = fallbacks
	return *chosen
}

// HealthReport returns current health status for all discovered drivers.
func (r *Router) HealthReport() []DriverHealth {
	return ReadAllHealth(r.healthDir)
}

// maxTierForBudget returns the highest tier to consider for a budget level.
func maxTierForBudget(budget string) CostTier {
	switch strings.ToLower(budget) {
	case "low":
		return TierLocal
	case "medium":
		return TierCLI
	case "high", "":
		return TierAPI
	default:
		return TierAPI
	}
}

// taskAffinityTier returns the minimum cost tier suitable for a given task type.
// Local models handle triage/classification; coding tasks need CLI-grade models;
// briefings and artifacts work well with subscription browser drivers.
func taskAffinityTier(taskType string) CostTier {
	lower := strings.ToLower(taskType)

	codingKeywords := []string{
		"code", "coding", "pr", "pull request", "commit", "review",
		"refactor", "debug", "implement", "fix bug", "test", "build",
		"deploy", "migration", "schema",
	}
	for _, kw := range codingKeywords {
		if strings.Contains(lower, kw) {
			return TierCLI
		}
	}

	briefingKeywords := []string{
		"briefing", "artifact", "summary", "document", "research",
		"report", "analysis", "notebooklm", "chatgpt",
	}
	for _, kw := range briefingKeywords {
		if strings.Contains(lower, kw) {
			return TierSubscription
		}
	}

	// Default: no minimum — cheapest available driver wins.
	return TierLocal
}

// tierFor returns the cost tier for a driver, defaulting to CLI.
func tierFor(driver string) CostTier {
	if t, ok := driverTiers[driver]; ok {
		return t
	}
	return TierCLI // unknown drivers default to CLI tier
}

// tierIndex returns the position in the cost cascade.
func tierIndex(t CostTier) int {
	for i, tier := range tierOrder {
		if tier == t {
			return i
		}
	}
	return len(tierOrder)
}
