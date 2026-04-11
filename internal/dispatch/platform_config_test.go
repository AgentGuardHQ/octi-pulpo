package dispatch

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPlatformConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "platforms.json")
	os.WriteFile(path, []byte(`{
		"priority": ["claude", "codex"],
		"platforms": {
			"claude": {"queues": ["intake","build"], "model": "opus", "daily_cap": 20, "enabled": true},
			"codex":  {"queues": ["intake"], "model": "o3", "daily_cap": 5, "enabled": false}
		}
	}`), 0644)

	cfg, err := LoadPlatformConfig(path)
	if err != nil {
		t.Fatalf("LoadPlatformConfig: %v", err)
	}
	if len(cfg.Priority) != 2 {
		t.Fatalf("expected 2 priorities, got %d", len(cfg.Priority))
	}
	if cfg.Priority[0] != "claude" {
		t.Errorf("expected first priority claude, got %s", cfg.Priority[0])
	}
	claude := cfg.Platforms["claude"]
	if !claude.Enabled {
		t.Error("expected claude enabled")
	}
	if len(claude.Queues) != 2 {
		t.Errorf("expected 2 queues for claude, got %d", len(claude.Queues))
	}
	codex := cfg.Platforms["codex"]
	if codex.Enabled {
		t.Error("expected codex disabled")
	}
}

func TestPlatformConfig_AcceptsQueue(t *testing.T) {
	p := PlatformEntry{Queues: []string{"intake", "groom"}, Enabled: true}
	if !p.AcceptsQueue("intake") {
		t.Error("expected intake accepted")
	}
	if p.AcceptsQueue("build") {
		t.Error("expected build rejected")
	}
}

func TestPlatformConfig_MissingPlatformInPriority(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "platforms.json")
	os.WriteFile(path, []byte(`{
		"priority": ["claude", "ghost"],
		"platforms": {
			"claude": {"queues": ["intake"], "model": "opus", "daily_cap": 20, "enabled": true}
		}
	}`), 0644)

	_, err := LoadPlatformConfig(path)
	if err == nil {
		t.Fatal("expected error for missing platform in priority list")
	}
}
