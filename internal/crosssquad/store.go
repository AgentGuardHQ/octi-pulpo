// Package crosssquad manages cross-squad work requests — agents can request
// help from other squads, check incoming requests, and fulfill them.
package crosssquad

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RequestType classifies what kind of work is being requested.
type RequestType string

const (
	TypeReport RequestType = "report"
	TypeQuery  RequestType = "query"
	TypeReview RequestType = "review"
	TypeFix    RequestType = "fix"
	TypeDeploy RequestType = "deploy"
)

// Status tracks request lifecycle.
type Status string

const (
	StatusPending   Status = "pending"
	StatusClaimed   Status = "claimed"
	StatusFulfilled Status = "fulfilled"
)

// Request is a cross-squad work item submitted by one agent, targeting another squad.
type Request struct {
	ID              string      `json:"id"`
	FromAgent       string      `json:"from_agent"`
	ToSquad         string      `json:"to_squad"`
	Type            RequestType `json:"type"`
	Description     string      `json:"description"`
	Priority        int         `json:"priority"` // 0=urgent, 1=high, 2=normal
	Status          Status      `json:"status"`
	DeadlineMinutes int         `json:"deadline_minutes,omitempty"`
	CreatedAt       string      `json:"created_at"`
	ClaimedAt       string      `json:"claimed_at,omitempty"`
	FulfilledAt     string      `json:"fulfilled_at,omitempty"`
	Result          string      `json:"result,omitempty"`
	PRNumber        int         `json:"pr_number,omitempty"`
	AgeMinutes      int         `json:"age_minutes,omitempty"` // computed on read, not stored
}

// Store persists cross-squad requests in Redis.
//
// Key schema:
//   - {ns}:crosssquad:req:{id}         — request JSON (TTL 48h)
//   - {ns}:crosssquad:squad:{squad}    — sorted set of pending request IDs (score = priority * 1e9 + unix_ms)
//   - {ns}:crosssquad:all              — sorted set of all pending request IDs
type Store struct {
	rdb *redis.Client
	ns  string
}

// New returns a Store backed by the given Redis client.
func New(rdb *redis.Client, namespace string) *Store {
	return &Store{rdb: rdb, ns: namespace}
}

// Submit stores a new cross-squad request and returns its generated ID.
func (s *Store) Submit(ctx context.Context, req Request) (string, error) {
	now := time.Now().UTC()
	req.ID = fmt.Sprintf("req-%s-%d", req.FromAgent, now.UnixMilli())
	req.Status = StatusPending
	req.CreatedAt = now.Format(time.RFC3339)
	req.AgeMinutes = 0

	data, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	// Score = priority tier (lower = more urgent) * 1e9 + unix_ms (FIFO within tier)
	score := float64(req.Priority)*1e9 + float64(now.UnixMilli())

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.reqKey(req.ID), data, 48*time.Hour)
	pipe.ZAdd(ctx, s.squadKey(req.ToSquad), redis.Z{Score: score, Member: req.ID})
	pipe.ZAdd(ctx, s.key("all"), redis.Z{Score: score, Member: req.ID})
	if _, err := pipe.Exec(ctx); err != nil {
		return "", fmt.Errorf("store request: %w", err)
	}
	return req.ID, nil
}

// ForSquad returns all pending or claimed requests targeting the given squad,
// ordered by priority (most urgent first).
func (s *Store) ForSquad(ctx context.Context, squad string) ([]Request, error) {
	ids, err := s.rdb.ZRange(ctx, s.squadKey(squad), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list squad requests: %w", err)
	}
	return s.loadRequests(ctx, ids)
}

// Fulfill marks a request as completed, records the result, and removes it
// from the pending sets. The result string describes what was produced (e.g. a
// report path or PR number). prNumber is optional (0 = no PR).
func (s *Store) Fulfill(ctx context.Context, requestID, result string, prNumber int) error {
	data, err := s.rdb.Get(ctx, s.reqKey(requestID)).Bytes()
	if err == redis.Nil {
		return fmt.Errorf("request %s not found", requestID)
	}
	if err != nil {
		return fmt.Errorf("get request: %w", err)
	}

	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return fmt.Errorf("unmarshal request: %w", err)
	}

	now := time.Now().UTC()
	req.Status = StatusFulfilled
	req.FulfilledAt = now.Format(time.RFC3339)
	req.Result = result
	req.PRNumber = prNumber

	updated, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal updated request: %w", err)
	}

	pipe := s.rdb.Pipeline()
	// Keep the record for 24h after fulfillment for observability
	pipe.Set(ctx, s.reqKey(requestID), updated, 24*time.Hour)
	// Remove from pending sets
	pipe.ZRem(ctx, s.squadKey(req.ToSquad), requestID)
	pipe.ZRem(ctx, s.key("all"), requestID)
	_, err = pipe.Exec(ctx)
	return err
}

// PendingSquads returns the names of squads that have at least one pending request.
// Used by the brain to decide which SR agents to wake up.
func (s *Store) PendingSquads(ctx context.Context) ([]string, error) {
	ids, err := s.rdb.ZRange(ctx, s.key("all"), 0, -1).Result()
	if err != nil {
		return nil, fmt.Errorf("list all pending: %w", err)
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Load requests to extract unique target squads
	requests, err := s.loadRequests(ctx, ids)
	if err != nil {
		return nil, err
	}

	seen := make(map[string]bool)
	var squads []string
	for _, r := range requests {
		if r.Status == StatusPending && !seen[r.ToSquad] {
			seen[r.ToSquad] = true
			squads = append(squads, r.ToSquad)
		}
	}
	return squads, nil
}

// Get returns a single request by ID.
func (s *Store) Get(ctx context.Context, requestID string) (*Request, error) {
	data, err := s.rdb.Get(ctx, s.reqKey(requestID)).Bytes()
	if err == redis.Nil {
		return nil, fmt.Errorf("request %s not found", requestID)
	}
	if err != nil {
		return nil, fmt.Errorf("get request: %w", err)
	}
	var req Request
	if err := json.Unmarshal(data, &req); err != nil {
		return nil, fmt.Errorf("unmarshal request: %w", err)
	}
	s.annotateAge(&req)
	return &req, nil
}

// loadRequests fetches and deserializes requests by ID, populating age. Skips
// IDs whose keys have expired (TTL race between sorted set and key).
func (s *Store) loadRequests(ctx context.Context, ids []string) ([]Request, error) {
	if len(ids) == 0 {
		return nil, nil
	}

	keys := make([]string, len(ids))
	for i, id := range ids {
		keys[i] = s.reqKey(id)
	}

	vals, err := s.rdb.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, fmt.Errorf("mget requests: %w", err)
	}

	var requests []Request
	for i, v := range vals {
		if v == nil {
			// Key expired — clean up stale sorted set entry
			s.rdb.ZRem(ctx, s.key("all"), ids[i])
			continue
		}
		var req Request
		if err := json.Unmarshal([]byte(v.(string)), &req); err != nil {
			continue
		}
		s.annotateAge(&req)
		requests = append(requests, req)
	}
	return requests, nil
}

func (s *Store) annotateAge(req *Request) {
	t, err := time.Parse(time.RFC3339, req.CreatedAt)
	if err == nil {
		req.AgeMinutes = int(time.Since(t).Minutes())
	}
}

func (s *Store) reqKey(id string) string    { return s.key("req:" + id) }
func (s *Store) squadKey(squad string) string { return s.key("squad:" + squad) }
func (s *Store) key(suffix string) string   { return s.ns + ":crosssquad:" + suffix }
