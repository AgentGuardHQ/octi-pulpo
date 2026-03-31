package dispatch

import (
	"fmt"
	"strings"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/pipeline"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

// FormatPipelineDashboard builds Slack Block Kit blocks showing pipeline stage
// depths, session counts, driver budgets, and backpressure status.
func FormatPipelineDashboard(
	depths map[pipeline.Stage]int,
	sessions map[pipeline.Stage]int,
	budgets []routing.DriverHealth,
	bp pipeline.BackpressureAction,
) []map[string]interface{} {
	now := time.Now().UTC().Format("15:04 UTC")
	var blocks []map[string]interface{}

	blocks = append(blocks, slackSection(fmt.Sprintf("*Pipeline Status (%s)*", now)))

	stages := []pipeline.Stage{
		pipeline.StageArchitect, pipeline.StageImplement,
		pipeline.StageQA, pipeline.StageReview, pipeline.StageRelease,
	}

	var lines []string
	for _, s := range stages {
		icon := stageIcon(s, depths[s], bp)
		name := strings.ToUpper(string(s))
		lines = append(lines, fmt.Sprintf("%s *%s*: %d queued, %d sessions", icon, name, depths[s], sessions[s]))
	}
	blocks = append(blocks, slackSection(strings.Join(lines, "\n")))

	if bp.PauseStage != "" || bp.ThrottleStage != "" {
		blocks = append(blocks, slackSection(fmt.Sprintf("Warning: *Backpressure:* %s", bp.Reason)))
	}

	if len(budgets) > 0 {
		var budgetLines []string
		for _, b := range budgets {
			icon := "G"
			pct := "?"
			if b.BudgetPct != nil {
				pct = fmt.Sprintf("%d%%", *b.BudgetPct)
				if *b.BudgetPct < 20 {
					icon = "R"
				} else if *b.BudgetPct < 50 {
					icon = "Y"
				}
			}
			if b.CircuitState == "OPEN" {
				icon = "R"
			}
			budgetLines = append(budgetLines, fmt.Sprintf("%s *%s*: %s remaining", icon, b.Name, pct))
		}
		blocks = append(blocks, slackSection("*Driver Budgets*\n"+strings.Join(budgetLines, "\n")))
	}

	return blocks
}

func stageIcon(s pipeline.Stage, depth int, bp pipeline.BackpressureAction) string {
	if bp.PauseStage == s {
		return "PAUSED"
	}
	if bp.ThrottleStage == s {
		return "SLOW"
	}
	if depth == 0 {
		return "o"
	}
	if depth > 8 {
		return "!"
	}
	if depth > 4 {
		return "~"
	}
	return "+"
}
