package org

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/redis/go-redis/v9"
)

// Agent represents an agent in the org chart.
type Agent struct {
	Name      string `json:"name"`
	Squad     string `json:"squad,omitempty"`
	Role      string `json:"role,omitempty"`
	ReportsTo string `json:"reports_to,omitempty"`
	Box       string `json:"box,omitempty"`
	Driver    string `json:"driver,omitempty"`
}

// OrgStore manages agent org chart records in Redis.
type OrgStore struct {
	rdb       *redis.Client
	namespace string
}

// NewOrgStore creates an OrgStore backed by the given Redis client.
func NewOrgStore(rdb *redis.Client, namespace string) *OrgStore {
	return &OrgStore{rdb: rdb, namespace: namespace}
}

// key builds a namespaced Redis key.
func (s *OrgStore) key(suffix string) string {
	return s.namespace + ":org:" + suffix
}

// Put stores an agent record, indexes the name, and tracks the reports-to
// relationship.
func (s *OrgStore) Put(ctx context.Context, agent Agent) error {
	data, err := json.Marshal(agent)
	if err != nil {
		return fmt.Errorf("org: marshal agent %q: %w", agent.Name, err)
	}

	pipe := s.rdb.Pipeline()
	pipe.Set(ctx, s.key("agents:"+agent.Name), data, 0)
	pipe.SAdd(ctx, s.key("agent-index"), agent.Name)
	if agent.ReportsTo != "" {
		pipe.SAdd(ctx, s.key("reports-to:"+agent.ReportsTo), agent.Name)
	}
	_, err = pipe.Exec(ctx)
	if err != nil {
		return fmt.Errorf("org: put agent %q: %w", agent.Name, err)
	}
	return nil
}

// Get retrieves a single agent by name.
func (s *OrgStore) Get(ctx context.Context, name string) (Agent, error) {
	data, err := s.rdb.Get(ctx, s.key("agents:"+name)).Result()
	if err != nil {
		return Agent{}, fmt.Errorf("org: get agent %q: %w", name, err)
	}
	var agent Agent
	if err := json.Unmarshal([]byte(data), &agent); err != nil {
		return Agent{}, fmt.Errorf("org: unmarshal agent %q: %w", name, err)
	}
	return agent, nil
}

// DirectReports returns the sorted list of agents that report to the given
// manager.
func (s *OrgStore) DirectReports(ctx context.Context, manager string) ([]string, error) {
	names, err := s.rdb.SMembers(ctx, s.key("reports-to:"+manager)).Result()
	if err != nil {
		return nil, fmt.Errorf("org: direct reports for %q: %w", manager, err)
	}
	sort.Strings(names)
	return names, nil
}

// ChainOfCommand walks the reports_to chain from the named agent up to the
// root. Returns the chain starting with the given name. Cycle-safe via a
// visited set.
func (s *OrgStore) ChainOfCommand(ctx context.Context, name string) ([]string, error) {
	var chain []string
	visited := make(map[string]bool)
	current := name

	for current != "" && !visited[current] {
		visited[current] = true
		chain = append(chain, current)

		agent, err := s.Get(ctx, current)
		if err != nil {
			// If we can't find the agent, stop the chain here.
			break
		}
		current = agent.ReportsTo
	}
	return chain, nil
}

// All returns every agent in the index.
func (s *OrgStore) All(ctx context.Context) ([]Agent, error) {
	names, err := s.rdb.SMembers(ctx, s.key("agent-index")).Result()
	if err != nil {
		return nil, fmt.Errorf("org: list agent index: %w", err)
	}
	sort.Strings(names)

	agents := make([]Agent, 0, len(names))
	for _, name := range names {
		agent, err := s.Get(ctx, name)
		if err != nil {
			continue // skip agents whose data is missing
		}
		agents = append(agents, agent)
	}
	return agents, nil
}
