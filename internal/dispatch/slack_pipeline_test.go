package dispatch

import (
	"strings"
	"testing"

	"github.com/AgentGuardHQ/octi-pulpo/internal/pipeline"
	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
)

func TestFormatPipelineDashboard(t *testing.T) {
	depths := map[pipeline.Stage]int{
		pipeline.StageArchitect: 1, pipeline.StageImplement: 5,
		pipeline.StageQA: 2, pipeline.StageReview: 3, pipeline.StageRelease: 0,
	}
	sessions := map[pipeline.Stage]int{
		pipeline.StageArchitect: 1, pipeline.StageImplement: 3,
		pipeline.StageQA: 1, pipeline.StageReview: 2,
	}
	pct := 65
	budgets := []routing.DriverHealth{
		{Name: "claude-code", CircuitState: "CLOSED", BudgetPct: &pct},
	}

	blocks := FormatPipelineDashboard(depths, sessions, budgets, pipeline.BackpressureAction{})

	if len(blocks) == 0 {
		t.Fatal("expected non-empty blocks")
	}
	raw := blocksToString(blocks)
	if !strings.Contains(raw, "ARCHITECT") {
		t.Error("expected ARCHITECT in dashboard")
	}
	if !strings.Contains(raw, "IMPLEMENT") {
		t.Error("expected IMPLEMENT in dashboard")
	}
}

func blocksToString(blocks []map[string]interface{}) string {
	var sb strings.Builder
	for _, b := range blocks {
		switch text := b["text"].(type) {
		case map[string]interface{}:
			if t, ok := text["text"].(string); ok {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		case map[string]string:
			if t, ok := text["text"]; ok {
				sb.WriteString(t)
				sb.WriteByte('\n')
			}
		}
	}
	return sb.String()
}
