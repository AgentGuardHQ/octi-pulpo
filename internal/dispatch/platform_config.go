package dispatch

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// PlatformEntry describes one agent CLI surface.
type PlatformEntry struct {
	Queues   []string `json:"queues"`
	Model    string   `json:"model"`
	DailyCap int      `json:"daily_cap"`
	Enabled  bool     `json:"enabled"`
}

// AcceptsQueue returns true if this platform is configured for the given queue.
func (p *PlatformEntry) AcceptsQueue(queue string) bool {
	for _, q := range p.Queues {
		if q == queue {
			return true
		}
	}
	return false
}

// PlatformConfig is the top-level structure of platforms.json.
type PlatformConfig struct {
	Priority  []string                 `json:"priority"`
	Platforms map[string]PlatformEntry `json:"platforms"`
}

// LoadPlatformConfig reads and validates a platforms.json file.
func LoadPlatformConfig(path string) (*PlatformConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read platform config: %w", err)
	}
	var cfg PlatformConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse platform config: %w", err)
	}
	// Validate: every priority entry must have a platform definition.
	for _, name := range cfg.Priority {
		if _, ok := cfg.Platforms[name]; !ok {
			return nil, fmt.Errorf("platform %q in priority list but not in platforms map", name)
		}
	}
	return &cfg, nil
}

// PlatformConfigHolder provides thread-safe access to a hot-reloadable platform config.
type PlatformConfigHolder struct {
	mu   sync.RWMutex
	cfg  *PlatformConfig
	path string
}

// NewPlatformConfigHolder loads the config from path and returns a holder.
func NewPlatformConfigHolder(path string) (*PlatformConfigHolder, error) {
	cfg, err := LoadPlatformConfig(path)
	if err != nil {
		return nil, err
	}
	return &PlatformConfigHolder{cfg: cfg, path: path}, nil
}

// Get returns the current config (read-locked).
func (h *PlatformConfigHolder) Get() *PlatformConfig {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.cfg
}

// Reload re-reads the config from disk. Called on SIGHUP.
func (h *PlatformConfigHolder) Reload() error {
	cfg, err := LoadPlatformConfig(h.path)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.cfg = cfg
	h.mu.Unlock()
	return nil
}
