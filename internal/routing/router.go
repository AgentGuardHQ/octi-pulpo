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
	// Subscription (browser-based, already paying)
	"openclaw": TierSubscription,
	// CLI (metered subscription)
	"claude-code": TierCLI,
	"copilot":     TierCLI,
	"codex":       TierCLI,
	"gemini":      TierCLI,
	"goose":       TierCLI,
	// API (per-token billing)
	"claude-api": TierAPI,
	"openai-api": TierAPI,
	"gemini-api": TierAPI,
}

// taskTypeFloors maps task-type keywords to the minimum cost tier appropriate
// for that kind of work. Tiers below the floor are skipped even if healthy.
//
// Ordering matters: first match wins. More specific entries should come first.
var taskTypeFloors = []struct {
	keywords []string
	floor    CostTier
}{
	// Coding work requires a capable code-aware driver — CLI minimum.
	{
		keywords: []string{
			"code-review", "coding", "refactor", "commit", "pr", "merge",
			"senior", "qa", "reviewer", "merger", "engineer", "test", "debug",
		},
		floor: TierCLI,
	},
	// Artifact and research tasks work well with subscription browser drivers.
	{
		keywords: []string{
			"artifact", "briefing", "research", "notebook", "report", "document",
		},
		floor: TierSubscription,
	},
	// Triage and classification are cheap — local models are sufficient.
	{
		keywords: []string{
			"triage", "classify", "classification", "summarize", "label", "simple",
		},
		floor: TierLocal,
	},
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
	Floor      string   `json:"floor,omitempty"` // minimum tier enforced by task type
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

// Recommend returns the cheapest healthy driver for the given task within the
// allowed tier window [floor, ceiling]:
//   - floor (from taskType): minimum tier appropriate for the work
//   - ceiling (from budget): "low" → local, "medium" → cli, "high"/"" → api
//
// Example: a "code-review" task with budget "high" uses the window [cli, api].
// Tiers below the floor are skipped even if healthy — routing ollama to a
// coding task would produce poor results regardless of availability.
func (r *Router) Recommend(taskType, budget string) RouteDecision {
	floor := taskFloor(taskType)
	ceiling := maxTierForBudget(budget)
	drivers := DiscoverDrivers(r.healthDir)

	// Build health map from discovered drivers.
	// Only drivers with health files on disk are candidates.
	healthMap := make(map[string]DriverHealth)
	for _, d := range drivers {
		healthMap[d] = ReadDriverHealth(r.healthDir, d)
	}

	var chosen *RouteDecision
	var fallbacks []string

	// Walk tiers in cost order within the [floor, ceiling] window.
	for _, tier := range tierOrder {
		if tierIndex(tier) < tierIndex(floor) {
			continue // below task-type floor
		}
		if tierIndex(tier) > tierIndex(ceiling) {
			break // above budget ceiling
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
				chosen = &RouteDecision{
					Driver:     name,
					Tier:       string(tier),
					Floor:      string(floor),
					Confidence: confidence,
					Reason:     fmt.Sprintf("cheapest healthy driver in window [%s, %s] (state: %s)", floor, ceiling, health.CircuitState),
				}
			} else {
				fallbacks = append(fallbacks, name)
			}
		}
	}

	if chosen == nil {
		return RouteDecision{
			Skip:   true,
			Floor:  string(floor),
			Reason: fmt.Sprintf("all drivers exhausted in window [%s, %s]", floor, ceiling),
		}
	}

	chosen.Fallbacks = fallbacks
	return *chosen
}

// HealthReport returns current health status for all discovered drivers.
func (r *Router) HealthReport() []DriverHealth {
	return ReadAllHealth(r.healthDir)
}

// taskFloor returns the minimum cost tier appropriate for the given task type.
// Tiers below the floor produce poor results for the task regardless of health.
// Unknown task types default to TierLocal so all healthy drivers are candidates.
func taskFloor(taskType string) CostTier {
	lower := strings.ToLower(taskType)
	for _, entry := range taskTypeFloors {
		for _, kw := range entry.keywords {
			if strings.Contains(lower, kw) {
				return entry.floor
			}
		}
	}
	return TierLocal // default: start from cheapest
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
