package sprint

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

// Goal represents a hierarchical objective in the sprint goal tree.
type Goal struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parent_id,omitempty"`
}

// GoalStore manages sprint goals in Redis.
type GoalStore struct {
	rdb       *redis.Client
	namespace string
}

// NewGoalStore creates a goal store backed by Redis.
func NewGoalStore(rdb *redis.Client, namespace string) *GoalStore {
	return &GoalStore{
		rdb:       rdb,
		namespace: namespace,
	}
}

// Put stores a goal in Redis and adds it to the goal index set.
func (gs *GoalStore) Put(ctx context.Context, goal Goal) error {
	data, err := json.Marshal(goal)
	if err != nil {
		return fmt.Errorf("marshal goal %s: %w", goal.ID, err)
	}

	pipe := gs.rdb.Pipeline()
	pipe.Set(ctx, gs.key("goals:"+goal.ID), data, 0)
	pipe.SAdd(ctx, gs.key("goal-index"), goal.ID)
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("store goal %s: %w", goal.ID, err)
	}
	return nil
}

// Get retrieves a goal by ID from Redis.
func (gs *GoalStore) Get(ctx context.Context, id string) (Goal, error) {
	raw, err := gs.rdb.Get(ctx, gs.key("goals:"+id)).Result()
	if err != nil {
		return Goal{}, fmt.Errorf("get goal %s: %w", id, err)
	}

	var goal Goal
	if err := json.Unmarshal([]byte(raw), &goal); err != nil {
		return Goal{}, fmt.Errorf("parse goal %s: %w", id, err)
	}
	return goal, nil
}

// Ancestry walks the parent chain from the given goal up to the root.
// Returns [self, parent, grandparent, ...]. Cycle-safe via visited set.
func (gs *GoalStore) Ancestry(ctx context.Context, goalID string) ([]string, error) {
	var chain []string
	visited := make(map[string]bool)
	current := goalID

	for current != "" {
		if visited[current] {
			break // cycle detected
		}
		visited[current] = true
		chain = append(chain, current)

		goal, err := gs.Get(ctx, current)
		if err != nil {
			// If we can't find the parent, stop the chain here
			if current != goalID {
				break
			}
			return nil, err
		}
		current = goal.ParentID
	}

	return chain, nil
}

// AncestryText returns a human-readable ancestry string in root-first order:
// "Root Name -> Parent Name -> Self Name"
func (gs *GoalStore) AncestryText(ctx context.Context, goalID string) (string, error) {
	chain, err := gs.Ancestry(ctx, goalID)
	if err != nil {
		return "", err
	}

	// Resolve names in reverse order (root first)
	names := make([]string, len(chain))
	for i, id := range chain {
		goal, err := gs.Get(ctx, id)
		if err != nil {
			names[i] = id // fallback to ID
		} else {
			names[i] = goal.Name
		}
	}

	// Reverse to get root → ... → self
	for i, j := 0, len(names)-1; i < j; i, j = i+1, j-1 {
		names[i], names[j] = names[j], names[i]
	}

	return strings.Join(names, " \u2192 "), nil
}

// key returns a namespaced Redis key for sprint goals.
func (gs *GoalStore) key(suffix string) string {
	return gs.namespace + ":sprint:" + suffix
}
