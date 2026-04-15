package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
)

// BackfillReport summarizes a retroactive agent_stats fill.
type BackfillReport struct {
	Entries          int            `json:"entries_scanned"`
	AgentsUpdated    int            `json:"agents_updated"`
	DispatchesAdded  int            `json:"dispatches_added"`
	SuccessesAdded   int            `json:"successes_added"`
	Skipped          int            `json:"skipped_already_counted"`
	MissingAgent     int            `json:"missing_agent_field"`
	NonDispatched    int            `json:"non_dispatched_result"`
	PerAgent         map[string]int `json:"per_agent"`
}

// backfillSeenKey tracks which dispatch-log entries have already been replayed
// into agent_stats, enabling idempotent re-runs. Fingerprints are agent|timestamp
// (good enough — DispatchRecord has no stable ID, and the same agent rarely
// dispatches twice in the same second).
func (d *Dispatcher) backfillSeenKey() string {
	return d.key("backfill:dispatch-log:seen")
}

// BackfillAgentStats replays every entry in dispatch-log into the agent_stats
// hashes so the leaderboard reflects the population that existed before the
// sink was wired (PR #233). Idempotent: a per-entry fingerprint is stored in a
// Redis set, so re-running skips entries already counted.
//
// Requires profiles to be set (d.profiles != nil); returns an error otherwise.
//
// Success signal: we treat result=="dispatched" as a dispatch. We do NOT bump
// successes_total here — success is a distinct signal (exit=0 + commits) that
// lives in worker-results, not dispatch-log. A worker-results backfill is a
// separate follow-up if leaderboard success counts are needed retroactively.
func (d *Dispatcher) BackfillAgentStats(ctx context.Context) (BackfillReport, error) {
	rep := BackfillReport{PerAgent: map[string]int{}}
	if d.profiles == nil {
		return rep, fmt.Errorf("backfill: profiles not set on dispatcher")
	}

	raw, err := d.rdb.LRange(ctx, d.key("dispatch-log"), 0, -1).Result()
	if err != nil {
		return rep, fmt.Errorf("lrange dispatch-log: %w", err)
	}
	rep.Entries = len(raw)

	touched := map[string]bool{}
	seenKey := d.backfillSeenKey()

	for _, r := range raw {
		var rec DispatchRecord
		if err := json.Unmarshal([]byte(r), &rec); err != nil {
			continue
		}
		if rec.Result != "dispatched" {
			rep.NonDispatched++
			continue
		}
		if rec.Agent == "" {
			rep.MissingAgent++
			continue
		}

		fp := rec.Agent + "|" + rec.Timestamp
		added, err := d.rdb.SAdd(ctx, seenKey, fp).Result()
		if err != nil {
			return rep, fmt.Errorf("sadd seen: %w", err)
		}
		if added == 0 {
			rep.Skipped++
			continue
		}

		if err := d.profiles.RecordDispatch(ctx, rec.Agent); err != nil {
			return rep, fmt.Errorf("record dispatch %s: %w", rec.Agent, err)
		}
		rep.DispatchesAdded++
		rep.PerAgent[rec.Agent]++
		touched[rec.Agent] = true
	}

	rep.AgentsUpdated = len(touched)
	return rep, nil
}
