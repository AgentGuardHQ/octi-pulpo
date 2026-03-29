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
	// Local ($0) — Ollama-served models, NemoClaw runtime
	"ollama":   TierLocal,
	"nemotron": TierLocal,
	// Subscription (already-paying browser sessions via OpenClaw)
	"openclaw":   TierSubscription,
	"chatgpt":    TierSubscription,
	"notebooklm": TierSubscription,
	"gemini-app": TierSubscription,
	// CLI (metered CLI tools, effectively already-paying)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token, burst capacity)
	"anthropic-api": TierAPI,
	"openai-api":    TierAPI,
	"gemini-api":    TierAPI,
}

// taskTypeMinTier maps task-type keywords to the minimum appropriate cost tier.
// Routing won't start below this tier for a matched task type.
// Uses substring matching against the lowercase task description.
var taskTypeMinTier = map[string]CostTier{
	// Coding tasks require a capable agent with code execution tools (CLI+)
	"code-review":    TierCLI,
	"coding":         TierCLI,
	"commit":         TierCLI,
	"refactor":       TierCLI,
	"debug":          TierCLI,
	"implementation": TierCLI,
	"pull-request":   TierCLI,
	// Artifact / document tasks benefit from web-connected subscription models
	"artifact":  TierSubscription,
	"briefing":  TierSubscription,
	"document":  TierSubscription,
	"notebook":  TierSubscription,
	"report":    TierSubscription,
	// Burst / batch workloads require direct API access
	"burst": TierAPI,
	"batch": TierAPI,
}

// minTierForTaskType returns the minimum appropriate cost tier for a task type.
// Scans the task description for known keywords and returns the highest
// (most expensive) minimum tier found. Defaults to TierLocal if no keyword matches.
func minTierForTaskType(taskType string) CostTier {
	lower := strings.ToLower(taskType)
	best := TierLocal
	for keyword, tier := range taskTypeMinTier {
		if strings.Contains(lower, keyword) && tierIndex(tier) > tierIndex(best) {
			best = tier
		}
	}
	return best
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
// Two constraints bound the tier cascade:
//   - minTier: derived from taskType — coding tasks require CLI+, briefings require Subscription+
//   - maxTier: derived from budget — "low" caps at local, "medium" caps at cli, "high" allows all
//
// If taskType requires a higher tier than budget allows, Skip is returned immediately
// with a reason describing the conflict. Otherwise the cascade walks from minTier to
// maxTier and returns the first healthy driver found.
//
// Budget values:
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers
//   - ""       -> all tiers (default)
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)
	minTier := minTierForTaskType(taskType)

	// Budget conflict: task capability requirement exceeds budget ceiling.
	if tierIndex(minTier) > tierIndex(maxTier) {
		return RouteDecision{
			Skip:   true,
			Reason: fmt.Sprintf("task %q requires %s tier but budget only allows up to %s", taskType, minTier, maxTier),
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

	// Walk tiers in cost order from minTier to maxTier.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // skip tiers below task's capability minimum
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
					Reason:     fmt.Sprintf("cheapest healthy driver (tier: %s, state: %s)", tier, health.CircuitState),
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
