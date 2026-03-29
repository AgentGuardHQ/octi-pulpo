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
	"ollama":    TierLocal,
	"nemotron":  TierLocal,
	"nemoclaw":  TierLocal, // Nemotron via NemoClaw runtime
	// Subscription (browser-based, already-paying)
	"openclaw": TierSubscription,
	// CLI (metered subscription/included)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token burst capacity)
	"anthropic": TierAPI,
	"openai":    TierAPI,
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
	MinTier    string   `json:"min_tier,omitempty"` // capability floor derived from task type
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
// Budget controls the ceiling (which tiers are affordable):
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers
//   - ""       -> all tiers (default)
//
// TaskType sets the capability floor — some tasks require drivers that
// local models cannot handle (e.g. browser automation needs subscription tier,
// code review needs a CLI-grade model). The cascade starts at the floor and
// works up through the budget ceiling: cheapest capable driver wins.
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)
	minTier := taskMinTier(taskType)
	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers from capability floor to budget ceiling: cheapest capable driver first.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // below task capability requirement
		}
		if tierIndex(tier) > tierIndex(maxTier) {
			break // exceeds budget
		}
		for name, health := range healthMap {
			driverTier := tierFor(name)
			if driverTier != tier {
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
				reason := fmt.Sprintf("cheapest capable driver (tier: %s, state: %s)", tier, health.CircuitState)
				if minTier != TierLocal {
					reason = fmt.Sprintf("cheapest capable driver for %q task (min-tier: %s, tier: %s, state: %s)", taskType, minTier, tier, health.CircuitState)
				}
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
					MinTier:    string(minTier),
					Confidence: confidence,
					Reason:     reason,
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}
	}

	if chosen == nil {
		reason := "all drivers exhausted — circuit breakers OPEN"
		if minTier != TierLocal {
			reason = fmt.Sprintf("all capable drivers exhausted (task %q requires %s+ tier)", taskType, minTier)
		}
		return RouteDecision{
			Skip:    true,
			MinTier: string(minTier),
			Reason:  reason,
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

// taskMinTier returns the minimum capability tier required for the task type.
// Local models handle simple text tasks; browser automation requires a subscription
// driver; complex coding/PR work needs a CLI-grade model; burst API use goes direct.
func taskMinTier(taskType string) CostTier {
	switch {
	case containsAny(taskType, "browse", "navigate", "form", "click", "web", "screenshot", "browser", "ui"):
		return TierSubscription
	case containsAny(taskType, "code", "coding", "pr", "review", "commit", "debug", "refactor", "implement", "merge"):
		return TierCLI
	case containsAny(taskType, "batch", "programmatic", "api-call", "export"):
		return TierAPI
	default:
		return TierLocal // simple tasks, triage, classification — local is fine
	}
}

// containsAny reports whether s contains any of the given keywords (case-insensitive).
func containsAny(s string, keywords ...string) bool {
	lower := strings.ToLower(s)
	for _, kw := range keywords {
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
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
