package crosssquad

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const requestTTL = 24 * time.Hour

// Request is a cross-squad work request submitted by one agent to another squad.
type Request struct {
	ID              string `json:"id"`
	FromAgent       string `json:"from_agent"`
	ToSquad         string `json:"to_squad"`
	Type            string `json:"type"`                      // report, query, review, fix, deploy
	Description     string `json:"description"`
	Priority        int    `json:"priority"`                  // 0=urgent, 1=high, 2=normal
	DeadlineMinutes int    `json:"deadline_minutes,omitempty"`
	Status          string `json:"status"`                    // pending, fulfilled
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
	FulfilledBy     string `json:"fulfilled_by,omitempty"`
	Result          string `json:"result,omitempty"`
	PRNumber        int    `json:"pr_number,omitempty"`
	AgeMinutes      int    `json:"age_minutes,omitempty"` // computed at read time, not stored
}

// Store manages cross-squad work requests in Redis.
type Store struct {
	rdb *redis.Client
	ns  string
}

// New creates a cross-squad request store backed by the given Redis client.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

// Submit creates a new cross-squad work request and enqueues it for the target squad.
// Score in the pending sorted set encodes urgency: priority*1e9 + unix_seconds
// so P0 (urgent) sorts before P1 (high) before P2 (normal), FIFO within the same priority.
func (s *Store) Submit(ctx context.Context, fromAgent, toSquad, reqType, description string, priority, deadlineMinutes int) (*Request, error) {
	req := &Request{
		ID:              fmt.Sprintf("req-%s-%d", fromAgent, time.Now().UnixMilli()),
		FromAgent:       fromAgent,
		ToSquad:         toSquad,
		Type:            reqType,
		Description:     description,
		Priority:        priority,
		DeadlineMinutes: deadlineMinutes,
		Status:          "pending",
		CreatedAt:       time.Now().UTC().Format(time.RFC3339),
		UpdatedAt:       time.Now().UTC().Format(time.RFC3339),
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	score := float64(priority)*1e9 + float64(time.Now().Unix())

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.key("request:"+req.ID), data, requestTTL)
	pipe.ZAdd(ctx, s.key("requests:pending:"+toSquad), redis.Z{Score: score, Member: req.ID})
	if _, err = pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("store request: %w", err)
	}

	return req, nil
}

// Pending returns all pending requests for a squad, sorted urgent-first.
// Requests whose TTL has expired are silently pruned from the index.
func (s *Store) Pending(ctx context.Context, toSquad string) ([]Request, error) {
	ids, err := s.rdb.ZRange(ctx, s.key("requests:pending:"+toSquad), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list pending requests: %w", err)
	}

	now := time.Now()
	var requests []Request
	for _, id := range ids {
		data, err := s.rdb.Get(ctx, s.key("request:"+id)).Bytes()
		if err != nil {
			// TTL expired — prune stale index entry.
			s.rdb.ZRem(ctx, s.key("requests:pending:"+toSquad), id) //nolint:errcheck
			continue
		}
		var req Request
		if err := json.Unmarshal(data, &req); err != nil {
			continue
		}
		if t, err := time.Parse(time.RFC3339, req.CreatedAt); err == nil {
			req.AgeMinutes = int(now.Sub(t).Minutes())
		}
		requests = append(requests, req)
	}
	return requests, nil
}

// Fulfill marks a request as fulfilled, records the result, and removes it from the
// pending index. The fulfilled record is retained for 1 hour for audit purposes.
func (s *Store) Fulfill(ctx context.Context, requestID, fulfilledBy, result string, prNumber int) (*Request, error) {
	data, err := s.rdb.Get(ctx, s.key("request:"+requestID)).Bytes()
	if err != nil {
		return nil, fmt.Errorf("request not found: %s", requestID)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}

	req.Status = "fulfilled"
	req.FulfilledBy = fulfilledBy
	req.Result = result
	req.PRNumber = prNumber
	req.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	// Clear computed field before storing.
	req.AgeMinutes = 0

	updated, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal updated request: %w", err)
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.key("request:"+requestID), updated, time.Hour)
	pipe.ZRem(ctx, s.key("requests:pending:"+req.ToSquad), requestID)
	if _, err = pipe.Exec(ctx); err != nil {
		return nil, fmt.Errorf("fulfill request: %w", err)
	}

	return &req, nil
}

func (s *Store) key(suffix string) string {
	return s.ns + ":" + suffix
}
