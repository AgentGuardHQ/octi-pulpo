package dispatch

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const defaultSkipThreshold = 3
const defaultSkipTTL = 24 * time.Hour

// SkipList tracks issues that no platform can dispatch.
// Uses in-memory maps with optional Redis persistence.
type SkipList struct {
	rdb       *redis.Client
	namespace string
	Threshold int
	TTL       time.Duration

	mu         sync.Mutex
	rejections map[string]int       // issue key -> consecutive rejection count
	skipped    map[string]time.Time // issue key -> time added to skip list
}

// NewSkipList creates a skip list. If rdb is nil, operates in-memory only.
func NewSkipList(rdb *redis.Client, namespace string) *SkipList {
	return &SkipList{
		rdb:        rdb,
		namespace:  namespace,
		Threshold:  defaultSkipThreshold,
		TTL:        defaultSkipTTL,
		rejections: make(map[string]int),
		skipped:    make(map[string]time.Time),
	}
}

// RecordRejection increments the rejection counter. If it hits the threshold,
// the issue is added to the skip list.
func (s *SkipList) RecordRejection(issueKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.rejections[issueKey]++
	if s.rejections[issueKey] >= s.Threshold {
		s.skipped[issueKey] = time.Now()
		if s.rdb != nil {
			ctx := context.Background()
			key := fmt.Sprintf("%s:skip-list", s.namespace)
			s.rdb.ZAdd(ctx, key, redis.Z{
				Score:  float64(time.Now().Unix()),
				Member: issueKey,
			})
		}
	}
}

// IsSkipped returns true if the issue is in the skip list.
func (s *SkipList) IsSkipped(issueKey string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.skipped[issueKey]
	return ok
}

// Clear removes an issue from the skip list and resets its rejection counter.
func (s *SkipList) Clear(issueKey string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.skipped, issueKey)
	delete(s.rejections, issueKey)
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.ZRem(ctx, key, issueKey)
	}
}

// ClearAll removes all entries from the skip list.
func (s *SkipList) ClearAll() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.skipped = make(map[string]time.Time)
	s.rejections = make(map[string]int)
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.Del(ctx, key)
	}
}

// ExpireOld removes entries older than TTL.
func (s *SkipList) ExpireOld() {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-s.TTL)
	for k, t := range s.skipped {
		if t.Before(cutoff) {
			delete(s.skipped, k)
			delete(s.rejections, k)
		}
	}
	if s.rdb != nil {
		ctx := context.Background()
		key := fmt.Sprintf("%s:skip-list", s.namespace)
		s.rdb.ZRemRangeByScore(ctx, key, "-inf", fmt.Sprintf("%d", cutoff.Unix()))
	}
}

// ListAll returns all currently skipped issue keys.
func (s *SkipList) ListAll() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var keys []string
	for k := range s.skipped {
		keys = append(keys, k)
	}
	return keys
}

// Size returns the number of skipped issues.
func (s *SkipList) Size() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.skipped)
}
