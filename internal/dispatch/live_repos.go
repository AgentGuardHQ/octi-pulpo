package dispatch

// LiveRepos is the authoritative list of repos that the dispatch layer
// routes to via adapters and webhooks. Used by signal-watcher inference
// and the fossil regression tests.
//
// Per-repo SR/EM/QA agent names were excised in octi#271 Phase 2+3 when
// dispatch collapsed to adapter-based routing (Clawta, GH Actions,
// Anthropic, OpenClaw). Regression coverage: fossil_regression_test.go.
var LiveRepos = []string{
	"kernel",
	"shellforge",
	"clawta",
	"sentinel",
	"llmint",
	"octi",
	"workspace",
	"ganglia",
}
