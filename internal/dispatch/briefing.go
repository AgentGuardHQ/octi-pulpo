package dispatch

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/AgentGuardHQ/octi-pulpo/internal/routing"
	"github.com/AgentGuardHQ/octi-pulpo/internal/sprint"
	"github.com/redis/go-redis/v9"
)

// WorkerResult is a single agent execution record from the worker-results list.
type WorkerResult struct {
	Agent       string  `json:"agent"`
	ExitCode    int     `json:"exit_code"`
	DurationSec float64 `json:"duration_sec"`
	HadCommits  bool    `json:"had_commits"`
	Timestamp   string  `json:"timestamp"`
}

// DailyBriefing is an aggregated daily swarm intelligence summary.
// It is the data layer for the CTO daily digest and the eventual NotebookLM export.
type DailyBriefing struct {
	Date        string                 `json:"date"`
	PassRate    float64                `json:"pass_rate"`    // 0–100
	WorkerOK    int64                  `json:"worker_ok"`
	WorkerFail  int64                  `json:"worker_fail"`
	Drivers     []routing.DriverHealth `json:"drivers"`
	ShippedPRs  []sprint.SprintItem    `json:"shipped_prs"`  // pr_open or done items with PR numbers
	OpenP0s     []sprint.SprintItem    `json:"open_p0s"`     // P0 items still open
	Blocked     []sprint.SprintItem    `json:"blocked"`      // open items whose depends_on are not done
	RecentFails []WorkerResult         `json:"recent_fails"` // latest failed worker runs (exit_code != 0)
}

// BuildDailyBriefing assembles a DailyBriefing from live Redis state and sprint items.
// It does not call external APIs; all data comes from the local Redis store.
func BuildDailyBriefing(ctx context.Context, rdb *redis.Client, ns string, drivers []routing.DriverHealth, items []sprint.SprintItem) DailyBriefing {
	b := DailyBriefing{
		Date:    time.Now().UTC().Format("2006-01-02"),
		Drivers: drivers,
	}

	// Pass rate from cumulative Redis counters.
	okStr, _ := rdb.Get(ctx, ns+":worker-ok").Result()
	failStr, _ := rdb.Get(ctx, ns+":worker-fail").Result()
	b.WorkerOK, _ = strconv.ParseInt(okStr, 10, 64)
	b.WorkerFail, _ = strconv.ParseInt(failStr, 10, 64)
	total := b.WorkerOK + b.WorkerFail
	if total > 0 {
		b.PassRate = float64(b.WorkerOK) / float64(total) * 100
	}

	// Recent failed runs (up to 10).
	raw, _ := rdb.LRange(ctx, ns+":worker-results", 0, 49).Result()
	for _, r := range raw {
		var wr WorkerResult
		if err := json.Unmarshal([]byte(r), &wr); err != nil {
			continue
		}
		if wr.ExitCode != 0 {
			b.RecentFails = append(b.RecentFails, wr)
			if len(b.RecentFails) >= 10 {
				break
			}
		}
	}

	// Sprint-derived metrics.
	doneSet := make(map[int]bool)
	for _, item := range items {
		if item.Status == "done" {
			doneSet[item.IssueNum] = true
		}
	}
	for _, item := range items {
		switch {
		case item.PRNumber > 0 && (item.Status == "pr_open" || item.Status == "done"):
			b.ShippedPRs = append(b.ShippedPRs, item)
		}
		if item.Priority == 0 && (item.Status == "open" || item.Status == "in_progress") {
			b.OpenP0s = append(b.OpenP0s, item)
		}
		if item.Status == "open" && len(item.DependsOn) > 0 {
			for _, dep := range item.DependsOn {
				if !doneSet[dep] {
					b.Blocked = append(b.Blocked, item)
					break
				}
			}
		}
	}

	return b
}
