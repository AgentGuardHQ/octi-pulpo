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
//
// The broadcast-channel surface was removed in octi#271 when the org
// collapsed to single-tenant. Only directed messaging and reply-wait remain.
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

// SendAndWait sends a directed message and blocks until a reply arrives on
// the reply channel, or timeout expires. Returns the reply content or error.
// Reply channel: {ns}:reply:{msg.FromContract}:{msg.ToContract}
func (b *Broker) SendAndWait(ctx context.Context, msg PeerMessage, timeout time.Duration) (string, error) {
	replyChannel := fmt.Sprintf("%s:reply:%s:%s", b.namespace, msg.FromContract, msg.ToContract)

	// Subscribe to the reply channel before sending so we don't miss the reply.
	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	pubsub := b.rdb.Subscribe(timeoutCtx, replyChannel)
	if _, err := pubsub.Receive(timeoutCtx); err != nil {
		pubsub.Close()
		return "", fmt.Errorf("messaging: subscribe reply channel %s: %w", replyChannel, err)
	}
	defer pubsub.Close()

	// Send the directed message.
	if err := b.SendDirected(ctx, msg); err != nil {
		return "", fmt.Errorf("messaging: SendAndWait send: %w", err)
	}

	// Wait for first message on reply channel or timeout.
	redisCh := pubsub.Channel()
	select {
	case raw, ok := <-redisCh:
		if !ok {
			return "", fmt.Errorf("messaging: reply channel closed unexpectedly")
		}
		return raw.Payload, nil
	case <-timeoutCtx.Done():
		return "", fmt.Errorf("messaging: SendAndWait timed out after %s waiting for reply from %s", timeout, msg.ToContract)
	}
}

// SendReply sends a reply to a specific message's reply channel.
// Publishes to {ns}:reply:{originalTo}:{originalFrom} (reversed — reply goes back to the original sender).
func (b *Broker) SendReply(ctx context.Context, originalFrom, originalTo, content string) error {
	// Note: reply channel is keyed from the original sender's perspective:
	// {ns}:reply:{originalFrom}:{originalTo}
	replyChannel := fmt.Sprintf("%s:reply:%s:%s", b.namespace, originalFrom, originalTo)
	if err := b.rdb.Publish(ctx, replyChannel, content).Err(); err != nil {
		return fmt.Errorf("messaging: SendReply to %s: %w", replyChannel, err)
	}
	return nil
}

// Subscribe returns a channel that receives directed messages for a given contract.
// Subscribes to {ns}:msg:{contractID}.
func (b *Broker) Subscribe(ctx context.Context, contractID string) (<-chan PeerMessage, error) {
	directedCh := fmt.Sprintf("%s:msg:%s", b.namespace, contractID)

	pubsub := b.rdb.Subscribe(ctx, directedCh)

	// Wait for subscription confirmation
	if _, err := pubsub.Receive(ctx); err != nil {
		pubsub.Close()
		return nil, fmt.Errorf("messaging: subscribe %s: %w", directedCh, err)
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
