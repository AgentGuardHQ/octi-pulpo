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
	// Local ($0) — Ollama and NemoClaw/Nemotron
	"ollama":    TierLocal,
	"nemotron":  TierLocal,
	"nemoclaw":  TierLocal,
	// Subscription — OpenClaw browser runtime driving web apps
	"openclaw":    TierSubscription,
	"chatgpt":     TierSubscription, // OpenClaw → ChatGPT web
	"notebooklm":  TierSubscription, // OpenClaw → NotebookLM
	"gemini-app":  TierSubscription, // OpenClaw → Gemini web app
	// CLI — metered tools running locally
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI, // Gemini CLI
	"goose":       TierCLI,
	// API — direct per-token calls, burst capacity
	"anthropic-api": TierAPI,
	"openai-api":    TierAPI,
	"gemini-api":    TierAPI,
}

// taskTypeMinTier maps task type keywords to the minimum capable tier.
// Tasks requiring strong reasoning or code generation should not be routed
// to local models that may lack the capability.
var taskTypeMinTier = map[string]CostTier{
	// Local-capable: triage, classification, simple summarization
	"triage":   TierLocal,
	"classify": TierLocal,
	"simple":   TierLocal,
	"label":    TierLocal,
	// Subscription-capable: briefings, artifacts, research reports
	"briefing":  TierSubscription,
	"artifact":  TierSubscription,
	"report":    TierSubscription,
	"research":  TierSubscription,
	"summarize": TierSubscription,
	// CLI-capable: code generation, PRs, commits, reviews
	"code":         TierCLI,
	"code-review":  TierCLI,
	"pr":           TierCLI,
	"commit":       TierCLI,
	"review":       TierCLI,
	"refactor":     TierCLI,
	"test":         TierCLI,
	"debug":        TierCLI,
	// API-only: burst/programmatic access
	"burst": TierAPI,
	"api":   TierAPI,
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

// Recommend returns the cheapest capable healthy driver for the given task.
//
// The budget parameter controls the maximum cost tier:
//   - "low"    -> local only
//   - "medium" -> local + subscription + cli
//   - "high"   -> all tiers (default)
//
// The taskType parameter sets the minimum capable tier — routing will not
// go below it even if cheaper drivers are available. For example, "code-review"
// requires at least a CLI-tier driver; routing to local is skipped.
// If taskType is empty or unrecognized, routing starts from the cheapest tier.
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	maxTier := maxTierForBudget(budget)
	minTier := minTierForTask(taskType)
	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order: cheapest capable first.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(minTier) {
			continue // below task capability floor
		}
		if tierIndex(tier) > tierIndex(maxTier) {
			break // above budget ceiling
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
				reason := fmt.Sprintf("cheapest capable driver (tier: %s, state: %s)", tier, health.CircuitState)
				if taskType != "" {
					reason = fmt.Sprintf("cheapest capable driver for %q (tier: %s, state: %s)", taskType, tier, health.CircuitState)
				}
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
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
		if taskType != "" && minTier != TierLocal {
			reason = fmt.Sprintf("all capable drivers for %q exhausted (min tier: %s)", taskType, minTier)
		}
		return RouteDecision{
			Skip:   true,
			Reason: reason,
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

// minTierForTask returns the minimum capable tier for a task type.
// Unrecognized task types default to TierLocal (no floor — cheapest wins).
func minTierForTask(taskType string) CostTier {
	if t, ok := taskTypeMinTier[strings.ToLower(taskType)]; ok {
		return t
	}
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
