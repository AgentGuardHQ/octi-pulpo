package dispatch

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// fossilAgentPatterns catches agent names from the squad era. Matches names
// ending in -sr, -em, or -qa (e.g. kernel-sr, shellforge-qa, cloud-em) and
// bare squad-fan-out roles (jared-conductor, director, hq-em).
//
// Per octi#271 Phase 2+3, no agent reference in production code may match
// these patterns. PR-review agents (workspace-pr-review-agent,
// analytics-pr-review-agent) and the merger (pr-merger-agent) are exempt.
var fossilAgentPatterns = []*regexp.Regexp{
	regexp.MustCompile(`-sr\b`),
	regexp.MustCompile(`-em\b`),
	regexp.MustCompile(`-qa\b`),
	regexp.MustCompile(`\bjared-conductor\b`),
	regexp.MustCompile(`\bdirector\b`),
}

// TestNoFossilAgentsInChains asserts DefaultChains has no squad-era agent
// as a key or as a chain target.
func TestNoFossilAgentsInChains(t *testing.T) {
	chains := DefaultChains()

	for key, action := range chains {
		for _, pat := range fossilAgentPatterns {
			if pat.MatchString(key) {
				t.Errorf("fossil agent %q found as DefaultChains key (matches %s) — squads are dead (octi#271)", key, pat)
			}
		}
		for _, bucket := range [][]string{action.OnSuccess, action.OnFailure, action.OnCommit} {
			for _, target := range bucket {
				for _, pat := range fossilAgentPatterns {
					if pat.MatchString(target) {
						t.Errorf("chain %q -> fossil %q (matches %s, octi#271)", key, target, pat)
					}
				}
			}
		}
	}
}

// TestNoFossilTimersInRules asserts DefaultRules registers no timer for a
// squad-era agent.
func TestNoFossilTimersInRules(t *testing.T) {
	rules := DefaultRules()
	for _, rule := range rules {
		if rule.EventType != EventTimer {
			continue
		}
		for _, pat := range fossilAgentPatterns {
			if pat.MatchString(rule.AgentName) {
				t.Errorf("timer rule registers fossil agent %q (matches %s, octi#271)", rule.AgentName, pat)
			}
		}
	}
}

// TestNoSquadStructFields scans production .go files under internal/ and
// asserts no struct declares a field named "Squad".
//
// This is a source-scan rather than a reflect-walk because many squad-
// bearing structs live behind interfaces or are never instantiated in
// tests. Grep-style checks catch regressions on lint-alike surfaces.
func TestNoSquadStructFields(t *testing.T) {
	root := findRepoRoot(t)
	internal := filepath.Join(root, "internal")

	// Match `Squad\s+string` at field-declaration positions (tab/space
	// indent followed by the field name and a type).
	fieldRe := regexp.MustCompile(`(?m)^\s+Squad\s+\w`)

	walk := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		if fieldRe.Match(data) {
			t.Errorf("squad struct field found in %s (octi#271)", path)
		}
		return nil
	}

	_ = filepath.Walk(internal, walk)
}

// TestNoSquadTermsInProdCode asserts no production .go file under internal/
// contains the literal string "squad" (case-insensitive) outside of comments
// referencing the octi#271 excision. Tightens the squad-era surface.
func TestNoSquadTermsInProdCode(t *testing.T) {
	root := findRepoRoot(t)
	internal := filepath.Join(root, "internal")

	// Allow references inside comment lines that explicitly mention the
	// excision — the word "octi#271" or "squad-era" (as in "removed in
	// octi#271 when the squad-era collapsed"). Non-comment lines must not
	// contain "squad" at all.
	squadWord := regexp.MustCompile(`(?i)\bsquad`)

	walk := func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if !squadWord.MatchString(line) {
				continue
			}
			trimmed := strings.TrimSpace(line)
			// Allow comment lines that explicitly reference the excision.
			if strings.HasPrefix(trimmed, "//") {
				if strings.Contains(trimmed, "octi#271") ||
					strings.Contains(trimmed, "squad-era") ||
					strings.Contains(trimmed, "squad pattern") ||
					strings.Contains(trimmed, "single-tenant") ||
					strings.Contains(trimmed, "collapsed") {
					continue
				}
			}
			t.Errorf("squad term found in %s:%d: %q — squads are dead (octi#271)",
				path, i+1, trimmed)
		}
		return nil
	}

	_ = filepath.Walk(internal, walk)
}

// findRepoRoot walks up from the current test's working directory until it
// finds a directory containing go.mod, and returns that directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", dir)
		}
		dir = parent
	}
}
