package messaging_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/messaging"
	"github.com/redis/go-redis/v9"
)

// newTestClient returns a Redis client and a unique namespace for the test.
// Skips the test if Redis is unavailable.
func newTestClient(t *testing.T) (*redis.Client, string) {
	t.Helper()
	rdb := redis.NewClient(&redis.Options{
		Addr: "localhost:6379",
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		t.Skipf("Redis unavailable (%v) — skipping", err)
	}
	ns := fmt.Sprintf("test:%s:%d", t.Name(), time.Now().UnixNano())
	t.Cleanup(func() { rdb.Close() })
	return rdb, ns
}

func TestBroker_SendAndReceive(t *testing.T) {
	rdb, ns := newTestClient(t)
	broker := messaging.NewBroker(rdb, ns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgCh, err := broker.Subscribe(ctx, "worker-b", "kernel")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}

	// Small delay to let the subscription register
	time.Sleep(50 * time.Millisecond)

	sent := messaging.PeerMessage{
		Timestamp:    time.Now().UTC(),
		FromContract: "worker-a",
		ToContract:   "worker-b",
		Content:      "hello from worker-a",
		Type:         "info",
	}

	if err := broker.SendDirected(ctx, sent); err != nil {
		t.Fatalf("SendDirected: %v", err)
	}

	select {
	case got := <-msgCh:
		if got.FromContract != sent.FromContract {
			t.Errorf("FromContract: got %q, want %q", got.FromContract, sent.FromContract)
		}
		if got.Content != sent.Content {
			t.Errorf("Content: got %q, want %q", got.Content, sent.Content)
		}
		if got.Type != "info" {
			t.Errorf("Type: got %q, want %q", got.Type, "info")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestBroker_RateLimit(t *testing.T) {
	rdb, ns := newTestClient(t)
	broker := messaging.NewBroker(rdb, ns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe so PUBLISH doesn't fail for no-subscribers
	_, err := broker.Subscribe(ctx, "target", "kernel")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	send := func(n int) error {
		return broker.SendDirected(ctx, messaging.PeerMessage{
			FromContract: "sender-rl",
			ToContract:   "target",
			Content:      fmt.Sprintf("msg %d", n),
			Type:         "info",
		})
	}

	// Messages 1-3 should succeed (default limit = 3)
	for i := 1; i <= 3; i++ {
		if err := send(i); err != nil {
			t.Fatalf("message %d should succeed, got: %v", i, err)
		}
	}

	// Message 4 should be rejected
	if err := send(4); err == nil {
		t.Fatal("message 4 should have been rejected by rate limit")
	}
}

func TestBroker_BroadcastRequiresApproval(t *testing.T) {
	rdb, ns := newTestClient(t)
	broker := messaging.NewBroker(rdb, ns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Subscribe to the broadcast channel for "kernel" squad
	broadcastCh, err := broker.Subscribe(ctx, "some-worker", "kernel")
	if err != nil {
		t.Fatalf("Subscribe broadcast: %v", err)
	}

	// Subscribe to the broadcast_request channel directly to verify routing
	requestCh := fmt.Sprintf("%s:broadcast_request:kernel", ns)
	psReq := rdb.Subscribe(ctx, requestCh)
	if _, err := psReq.Receive(ctx); err != nil {
		t.Fatalf("subscribe broadcast_request: %v", err)
	}
	defer psReq.Close()

	time.Sleep(50 * time.Millisecond)

	msg := messaging.PeerMessage{
		FromContract: "worker-x",
		Content:      "important announcement",
		Type:         "warning",
	}

	if err := broker.RequestBroadcast(ctx, msg, "kernel"); err != nil {
		t.Fatalf("RequestBroadcast: %v", err)
	}

	// The request should arrive on the broadcast_request channel
	select {
	case raw := <-psReq.Channel():
		if !strings.Contains(raw.Payload, "important announcement") {
			t.Errorf("expected announcement in request payload, got: %s", raw.Payload)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for broadcast request")
	}

	// The broadcast channel should NOT have received anything yet
	select {
	case got := <-broadcastCh:
		t.Errorf("broadcast channel should be empty before approval, got: %+v", got)
	default:
		// expected: nothing received
	}
}

func TestBroker_ContentTruncation(t *testing.T) {
	rdb, ns := newTestClient(t)
	broker := messaging.NewBroker(rdb, ns)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	msgCh, err := broker.Subscribe(ctx, "recv", "kernel")
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	// Build a 600-char string
	longContent := strings.Repeat("x", 600)

	if err := broker.SendDirected(ctx, messaging.PeerMessage{
		FromContract: "sender-trunc",
		ToContract:   "recv",
		Content:      longContent,
		Type:         "info",
	}); err != nil {
		t.Fatalf("SendDirected: %v", err)
	}

	select {
	case got := <-msgCh:
		if len(got.Content) != 500 {
			t.Errorf("expected content truncated to 500 chars, got %d", len(got.Content))
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for message")
	}
}

func TestBroker_InvalidType(t *testing.T) {
	rdb, ns := newTestClient(t)
	broker := messaging.NewBroker(rdb, ns)

	ctx := context.Background()

	err := broker.SendDirected(ctx, messaging.PeerMessage{
		FromContract: "worker-a",
		ToContract:   "worker-b",
		Content:      "something",
		Type:         "shout", // invalid
	})
	if err == nil {
		t.Fatal("expected error for invalid type, got nil")
	}
	if !strings.Contains(err.Error(), "invalid type") {
		t.Errorf("expected 'invalid type' in error, got: %v", err)
	}
}
