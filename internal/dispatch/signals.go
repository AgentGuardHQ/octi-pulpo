package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"
)

// SignalWatcher subscribes to Redis pub/sub for coordination signals
// and dispatches agents based on signal type.
//
// Agents broadcast signals via the coord_signal MCP tool. The watcher
// reacts to those signals and triggers follow-up agents through the dispatcher.
//
// The squad-era per-repo senior fan-out was excised in octi#271 Phase 2+3 —
// the watcher now only handles "blocked" (triage) and logs completion/
// directive signals for observability.
type SignalWatcher struct {
	dispatcher *Dispatcher
	rdb        *redis.Client
	namespace  string
	log        *log.Logger

	// triageAgents maps repo names to triage agents dispatched on "blocked"
	// signals. Kept as a minimal surface so the blocker path still has a
	// clear hook if a repo wants to register a CI-failure triage agent.
	triageAgents map[string]string
}

// NewSignalWatcher creates a signal watcher connected to Redis pub/sub.
func NewSignalWatcher(dispatcher *Dispatcher, rdb *redis.Client, namespace string) *SignalWatcher {
	return &SignalWatcher{
		dispatcher: dispatcher,
		rdb:        rdb,
		namespace:  namespace,
		log:        log.New(os.Stderr, "signal-watcher: ", log.LstdFlags),
		triageAgents: map[string]string{
			"kernel": "triage-failing-ci-agent",
		},
	}
}

// signalPayload is the parsed signal from Redis pub/sub.
type signalPayload struct {
	AgentID   string `json:"agent_id"`
	Type      string `json:"type"`    // completed, blocked, need-help, directive
	Payload   string `json:"payload"`
	Repo      string `json:"repo"`    // optional repo context (e.g. "chitinhq/kernel")
	Timestamp string `json:"timestamp"`
}

// Watch subscribes to the coordination signal channel and dispatches
// agents in response to signals. Blocks until context is cancelled.
func (sw *SignalWatcher) Watch(ctx context.Context) error {
	channel := sw.namespace + ":signal-stream"
	pubsub := sw.rdb.Subscribe(ctx, channel)
	defer pubsub.Close()

	_, err := pubsub.Receive(ctx)
	if err != nil {
		return fmt.Errorf("subscribe to %s: %w", channel, err)
	}

	sw.log.Printf("subscribed to %s", channel)

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-ch:
			if !ok {
				return nil
			}
			sw.handleSignal(ctx, msg.Payload)
		}
	}
}

// handleSignal parses a signal message and dispatches appropriate agents.
func (sw *SignalWatcher) handleSignal(ctx context.Context, raw string) {
	var sig signalPayload
	if err := json.Unmarshal([]byte(raw), &sig); err != nil {
		sw.log.Printf("parse error: %v", err)
		return
	}

	sw.log.Printf("received %s from %s: %s", sig.Type, sig.AgentID, sig.Payload)

	switch sig.Type {
	case "need-help":
		// Per-repo senior fan-out removed in octi#271. need-help is now
		// observability-only; re-surface via adapters if a live routing
		// target is reintroduced.
		sw.log.Printf("need-help signal from %s (no senior routing after octi#271)", sig.AgentID)
	case "blocked":
		sw.handleBlocked(ctx, sig)
	case "directive":
		sw.log.Printf("directive signal from %s (fan-out removed in octi#271)", sig.AgentID)
	case "completed":
		sw.log.Printf("completion signal from %s (handled by chains)", sig.AgentID)
	default:
		sw.log.Printf("unhandled signal type %q from %s", sig.Type, sig.AgentID)
	}
}

// handleBlocked dispatches the repo's triage agent.
func (sw *SignalWatcher) handleBlocked(ctx context.Context, sig signalPayload) {
	repo := inferRepo(sig.AgentID)
	triage, ok := sw.triageAgents[repo]
	if !ok {
		sw.log.Printf("no triage agent for repo %q (agent: %s)", repo, sig.AgentID)
		return
	}

	event := Event{
		Type:   EventSignal,
		Source: sig.AgentID,
		Repo:   sig.Repo,
		Payload: map[string]string{
			"signal_type":    "blocked",
			"from_agent":     sig.AgentID,
			"blocker_detail": sig.Payload,
		},
		Priority: 1,
	}

	result, err := sw.dispatcher.Dispatch(ctx, event, triage, 1)
	if err != nil {
		sw.log.Printf("dispatch %s for blocked: %v", triage, err)
		return
	}
	sw.log.Printf("blocked -> dispatched %s (%s)", triage, result.Action)
}

// inferRepo extracts the repo name from an agent ID. Matches a LiveRepos
// prefix or suffix; falls back to the first "-" segment.
func inferRepo(agentID string) string {
	for _, repo := range LiveRepos {
		if strings.HasPrefix(agentID, repo+"-") {
			return repo
		}
	}
	for _, repo := range LiveRepos {
		if strings.HasSuffix(agentID, "-"+repo) {
			return repo
		}
	}
	parts := strings.SplitN(agentID, "-", 2)
	return parts[0]
}
