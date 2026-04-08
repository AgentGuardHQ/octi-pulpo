package messaging

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// PeerMessage is a message exchanged between worker contracts.
type PeerMessage struct {
	Timestamp    time.Time `json:"ts"`
	FromContract string    `json:"from_contract"`
	ToContract   string    `json:"to_contract"` // empty for broadcast
	Content      string    `json:"content"`     // max 500 chars
	Type         string    `json:"type"`        // "info" | "dependency" | "warning"
}

// validTypes is the set of allowed message types.
var validTypes = map[string]bool{
	"info":       true,
	"dependency": true,
	"warning":    true,
}

// Broker manages peer-to-peer messaging between worker contracts via Redis pub/sub.
type Broker struct {
	rdb       *redis.Client
	namespace string
	rateLimit int // max messages per worker per task, default 3
}

// NewBroker creates a message broker. Default rate limit is 3 per task.
func NewBroker(rdb *redis.Client, namespace string) *Broker {
	return &Broker{
		rdb:       rdb,
		namespace: namespace,
		rateLimit: 3,
	}
}

// SendDirected sends a message to a specific contract's channel.
// Channel: {ns}:msg:{to_contract}
// Enforces rate limit per from_contract.
func (b *Broker) SendDirected(ctx context.Context, msg PeerMessage) error {
	if !validTypes[msg.Type] {
		return fmt.Errorf("messaging: invalid type %q: must be info, dependency, or warning", msg.Type)
	}

	// Truncate content silently
	if len(msg.Content) > 500 {
		msg.Content = msg.Content[:500]
	}

	// Rate limit check: INCR key, set TTL 1 hour on first INCR
	rateKey := fmt.Sprintf("%s:msg_count:%s", b.namespace, msg.FromContract)
	count, err := b.rdb.Incr(ctx, rateKey).Result()
	if err != nil {
		return fmt.Errorf("messaging: rate limit check: %w", err)
	}
	if count == 1 {
		// First message: set TTL of 1 hour
		b.rdb.Expire(ctx, rateKey, time.Hour)
	}
	if count > int64(b.rateLimit) {
		return fmt.Errorf("messaging: rate limit exceeded for %s (%d/%d)", msg.FromContract, count, b.rateLimit)
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("messaging: marshal: %w", err)
	}

	channel := fmt.Sprintf("%s:msg:%s", b.namespace, msg.ToContract)
	if err := b.rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
		return fmt.Errorf("messaging: publish to %s: %w", channel, err)
	}
	return nil
}

// RequestBroadcast sends a broadcast request for brain approval.
// Channel: {ns}:broadcast_request:{squad}
// Does NOT deliver to workers directly.
func (b *Broker) RequestBroadcast(ctx context.Context, msg PeerMessage, squad string) error {
	if !validTypes[msg.Type] {
		return fmt.Errorf("messaging: invalid type %q: must be info, dependency, or warning", msg.Type)
	}

	if len(msg.Content) > 500 {
		msg.Content = msg.Content[:500]
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("messaging: marshal: %w", err)
	}

	channel := fmt.Sprintf("%s:broadcast_request:%s", b.namespace, squad)
	if err := b.rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
		return fmt.Errorf("messaging: publish broadcast request to %s: %w", channel, err)
	}
	return nil
}

// ApproveBroadcast forwards an approved broadcast to the squad channel.
// Channel: {ns}:broadcast:{squad}
func (b *Broker) ApproveBroadcast(ctx context.Context, msg PeerMessage, squad string) error {
	if !validTypes[msg.Type] {
		return fmt.Errorf("messaging: invalid type %q: must be info, dependency, or warning", msg.Type)
	}

	if len(msg.Content) > 500 {
		msg.Content = msg.Content[:500]
	}

	if msg.Timestamp.IsZero() {
		msg.Timestamp = time.Now().UTC()
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("messaging: marshal: %w", err)
	}

	channel := fmt.Sprintf("%s:broadcast:%s", b.namespace, squad)
	if err := b.rdb.Publish(ctx, channel, string(data)).Err(); err != nil {
		return fmt.Errorf("messaging: publish broadcast to %s: %w", channel, err)
	}
	return nil
}

// Subscribe returns a channel that receives messages for a given contract.
// Subscribes to both directed ({ns}:msg:{contractID}) and broadcast ({ns}:broadcast:{squad}) channels.
func (b *Broker) Subscribe(ctx context.Context, contractID string, squad string) (<-chan PeerMessage, error) {
	directedCh := fmt.Sprintf("%s:msg:%s", b.namespace, contractID)
	broadcastCh := fmt.Sprintf("%s:broadcast:%s", b.namespace, squad)

	pubsub := b.rdb.Subscribe(ctx, directedCh, broadcastCh)

	// Wait for subscription confirmation
	if _, err := pubsub.Receive(ctx); err != nil {
		pubsub.Close()
		return nil, fmt.Errorf("messaging: subscribe %s/%s: %w", directedCh, broadcastCh, err)
	}

	out := make(chan PeerMessage, 64)

	go func() {
		defer pubsub.Close()
		defer close(out)

		redisCh := pubsub.Channel()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-redisCh:
				if !ok {
					return
				}
				var pm PeerMessage
				if err := json.Unmarshal([]byte(msg.Payload), &pm); err != nil {
					// Skip malformed messages
					continue
				}
				select {
				case out <- pm:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return out, nil
}
