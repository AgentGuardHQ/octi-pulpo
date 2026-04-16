package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/chitinhq/octi-pulpo/internal/admission"
	"github.com/chitinhq/octi-pulpo/internal/bootcheck"
	"github.com/chitinhq/octi-pulpo/internal/budget"
	"github.com/chitinhq/octi-pulpo/internal/coordination"
	"github.com/chitinhq/octi-pulpo/internal/dispatch"
	"github.com/chitinhq/octi-pulpo/internal/mcptrace"
	"github.com/chitinhq/octi-pulpo/internal/memory"
	"github.com/chitinhq/octi-pulpo/internal/routing"
	"github.com/chitinhq/octi-pulpo/internal/sprint"
	"github.com/redis/go-redis/v9"
)

// ToolDef describes an MCP tool for the ListTools response.
type ToolDef struct {
	Name        string      `json:"name"`
	Description string      `json:"description"`
	InputSchema interface{} `json:"inputSchema"`
}

// Request is a JSON-RPC 2.0 request from the MCP client.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      interface{}     `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// Response is a JSON-RPC 2.0 response.
type Response struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      interface{} `json:"id"`
	Result  interface{} `json:"result,omitempty"`
	Error   *RPCError   `json:"error,omitempty"`
}

// RPCError is a JSON-RPC error.
type RPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Server is the Octi Pulpo MCP server.
//
// The squad-era standup, org-chart, and cross-squad request surfaces were
// deleted in octi#271 Phase 2+3 when dispatch collapsed to adapter-based
// routing. Memory is flat (no per-scope namespacing), sprint items key off
// repo alone, and coordination requests no longer exist.
type Server struct {
	mem              *memory.Store
	coord            *coordination.Engine
	router           *routing.Router
	dispatcher       *dispatch.Dispatcher
	sprintStore      *sprint.Store
	benchmark        *dispatch.BenchmarkTracker
	profiles         *dispatch.ProfileStore
	budgetStore      *budget.BudgetStore
	goalStore        *sprint.GoalStore
	admissionGate    *admission.Gate
	anthropicAdapter *dispatch.AnthropicAdapter
	ghActionsAdapter *dispatch.GHActionsAdapter
	copilotAdapter   *dispatch.CopilotAdapter
	promptCLIAdapter *dispatch.PromptCLIAdapter
	rdb              *redis.Client
	redisNS          string
	bootcheckCache   *bootcheck.Cache
	gateRunner       GateRunner
}

// SetBootcheckCache enables the bootcheck_status MCP tool.
func (s *Server) SetBootcheckCache(c *bootcheck.Cache) { s.bootcheckCache = c }

// New creates an MCP server backed by the given memory and coordination engines.
func New(mem *memory.Store, coord *coordination.Engine, router *routing.Router) *Server {
	return &Server{mem: mem, coord: coord, router: router}
}

// SetDispatcher adds dispatch capabilities to the MCP server.
func (s *Server) SetDispatcher(d *dispatch.Dispatcher) {
	s.dispatcher = d
}

// SetSprintStore enables sprint-related MCP tools.
func (s *Server) SetSprintStore(ss *sprint.Store) {
	s.sprintStore = ss
}

// SetBenchmark enables throughput metrics MCP tools.
func (s *Server) SetBenchmark(bt *dispatch.BenchmarkTracker) {
	s.benchmark = bt
}

// SetProfileStore enables the agent leaderboard MCP tool.
func (s *Server) SetProfileStore(ps *dispatch.ProfileStore) {
	s.profiles = ps
}

// SetRedis enables Redis-backed budget enrichment for the health_report tool.
func (s *Server) SetRedis(rdb *redis.Client, ns string) {
	s.rdb = rdb
	s.redisNS = ns
}

func (s *Server) SetBudgetStore(b *budget.BudgetStore) {
	s.budgetStore = b
}

func (s *Server) SetGoalStore(g *sprint.GoalStore) {
	s.goalStore = g
}

// SetAdmissionGate enables admission control MCP tools.
func (s *Server) SetAdmissionGate(g *admission.Gate) {
	s.admissionGate = g
}

func (s *Server) SetAnthropicAdapter(a *dispatch.AnthropicAdapter) { s.anthropicAdapter = a }
func (s *Server) SetGHActionsAdapter(a *dispatch.GHActionsAdapter) { s.ghActionsAdapter = a }
func (s *Server) SetCopilotAdapter(a *dispatch.CopilotAdapter)     { s.copilotAdapter = a }

// SetPromptCLIAdapter enables the dispatch_prompt_to_cli MCP tool.
func (s *Server) SetPromptCLIAdapter(a *dispatch.PromptCLIAdapter) { s.promptCLIAdapter = a }

// Serve runs the MCP server on stdio (stdin/stdout JSON-RPC).
func (s *Server) Serve() error {
	decoder := json.NewDecoder(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)

	for {
		var req Request
		if err := decoder.Decode(&req); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("decode request: %w", err)
		}

		if req.ID == nil {
			continue
		}
		resp := s.handle(req)
		if err := encoder.Encode(resp); err != nil {
			return fmt.Errorf("encode response: %w", err)
		}
	}
}

func (s *Server) handle(req Request) Response {
	switch req.Method {
	case "initialize":
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{
			"protocolVersion": "2024-11-05",
			"serverInfo":      map[string]string{"name": "octi-pulpo", "version": "0.1.0"},
			"capabilities":    map[string]interface{}{"tools": map[string]interface{}{}},
		}}
	case "tools/list":
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{"tools": toolDefs()}}
	case "tools/call":
		return s.handleToolCall(req)
	default:
		return Response{JSONRPC: "2.0", ID: req.ID, Result: map[string]interface{}{}}
	}
}

func (s *Server) handleToolCall(req Request) (resp Response) {
	var params struct {
		Name      string          `json:"name"`
		Arguments json.RawMessage `json:"arguments"`
	}
	if err := json.Unmarshal(req.Params, &params); err != nil {
		return errorResp(req.ID, -32602, "invalid params")
	}

	ctx := context.Background()
	agentID := os.Getenv("CHITIN_AGENT_NAME")
	if agentID == "" {
		agentID = "unknown"
	}

	start := time.Now()
	defer func() {
		outcome, reason := "allow", ""
		if resp.Error != nil {
			outcome = "deny"
			reason = resp.Error.Message
		}
		mcptrace.Emit("octi", agentID, params.Name, outcome, reason, start)
	}()

	switch params.Name {
	case "memory_store":
		var args struct {
			Content string   `json:"content"`
			Topics  []string `json:"topics"`
		}
		json.Unmarshal(params.Arguments, &args)
		id, err := s.mem.Put(ctx, agentID, args.Content, args.Topics)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("Stored memory %s (topics: %s)", id, strings.Join(args.Topics, ", "))
		return textResult(req.ID, msg)

	case "memory_recall":
		var args struct {
			Query string `json:"query"`
			Limit int    `json:"limit"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Limit == 0 {
			args.Limit = 5
		}
		results, err := s.mem.Recall(ctx, args.Query, args.Limit)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(results) == 0 {
			return textResult(req.ID, "No relevant memories found.")
		}
		var lines []string
		for i, m := range results {
			lines = append(lines, fmt.Sprintf("%d. [%s] %s (topics: %s)", i+1, m.AgentID, m.Content, strings.Join(m.Topics, ", ")))
		}
		return textResult(req.ID, strings.Join(lines, "\n"))

	case "memory_status":
		claims, err := s.coord.ActiveClaims(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(claims) == 0 {
			return textResult(req.ID, "No agents have active claims right now.")
		}
		var lines []string
		for _, c := range claims {
			lines = append(lines, fmt.Sprintf("- %s: %s (claimed %s)", c.AgentID, c.Task, c.ClaimedAt))
		}
		return textResult(req.ID, strings.Join(lines, "\n"))

	case "tier_activity":
		if s.rdb == nil {
			return errorResp(req.ID, -32000, "redis not configured")
		}
		var args struct {
			WindowHours int `json:"windowHours"`
			Limit       int `json:"limit"`
		}
		_ = json.Unmarshal(params.Arguments, &args)
		if args.WindowHours == 0 {
			args.WindowHours = 24
		}
		if args.Limit == 0 {
			args.Limit = 500
		}
		summary, err := tierActivitySummary(ctx, s.rdb, s.redisNS, args.WindowHours, args.Limit)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		out, _ := json.MarshalIndent(summary, "", "  ")
		return textResult(req.ID, string(out))

	case "coord_claim":
		var args struct {
			Task       string `json:"task"`
			TTLSeconds int    `json:"ttlSeconds"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.TTLSeconds == 0 {
			args.TTLSeconds = 900
		}
		claim, err := s.coord.ClaimTask(ctx, agentID, args.Task, args.TTLSeconds)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Claimed: %q (expires in %ds)", claim.Task, claim.TTLSeconds))

	case "coord_signal":
		var args struct {
			Type    string `json:"type"`
			Payload string `json:"payload"`
		}
		json.Unmarshal(params.Arguments, &args)
		if err := s.coord.Broadcast(ctx, agentID, args.Type, args.Payload); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Signal broadcast: %s — %s", args.Type, args.Payload))

	case "route_recommend":
		var args struct {
			TaskDescription string `json:"taskDescription"`
			Budget          string `json:"budget"`
		}
		json.Unmarshal(params.Arguments, &args)
		dec := s.router.Recommend(args.TaskDescription, args.Budget)
		data, _ := json.Marshal(dec)
		return textResult(req.ID, string(data))

	case "health_report":
		report := s.enrichHealthReport(ctx, s.router.HealthReport())
		data, _ := json.Marshal(report)
		return textResult(req.ID, string(data))

	case "dispatch_event":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		var args struct {
			EventType string            `json:"eventType"`
			Source    string            `json:"source"`
			Repo      string            `json:"repo"`
			Payload   map[string]string `json:"payload"`
			Priority  int               `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.EventType == "" {
			return errorResp(req.ID, -32602, "eventType is required")
		}
		event := dispatch.Event{
			Type:     dispatch.EventType(args.EventType),
			Source:   args.Source,
			Repo:     args.Repo,
			Payload:  args.Payload,
			Priority: args.Priority,
		}
		results, err := s.dispatcher.DispatchEvent(ctx, event)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(results)
		return textResult(req.ID, string(data))

	case "dispatch_status":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		depth, _ := s.dispatcher.PendingCount(ctx)
		agents, _ := s.dispatcher.PendingAgents(ctx)
		recent, _ := s.dispatcher.RecentDispatches(ctx, 10)

		status := map[string]interface{}{
			"queue_depth":       depth,
			"pending_agents":    agents,
			"recent_dispatches": recent,
		}
		if sc := s.dispatcher.SwarmCircuit(); sc != nil {
			status["swarm_circuit"] = sc.Snapshot()
		} else {
			status["swarm_circuit"] = map[string]interface{}{"paused": false}
		}
		if sessionsInfo := loadChitinSessionSnapshot(ctx); sessionsInfo != nil {
			status["chitin_sessions"] = sessionsInfo
		}
		data, _ := json.Marshal(status)
		return textResult(req.ID, string(data))

	case "swarm_today":
		var args struct {
			WindowHours int `json:"window_hours"`
		}
		json.Unmarshal(params.Arguments, &args)
		rep := s.SwarmToday(ctx, args.WindowHours)
		data, _ := json.Marshal(rep)
		return textResult(req.ID, rep.Text+"\n"+string(data))

	case "dispatch_trigger":
		if s.dispatcher == nil {
			return errorResp(req.ID, -32000, "dispatcher not initialized")
		}
		var args struct {
			Agent    string `json:"agent"`
			Priority int    `json:"priority"`
			Budget   string `json:"budget"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent == "" {
			return errorResp(req.ID, -32602, "agent name is required")
		}
		event := dispatch.Event{
			Type:    dispatch.EventManual,
			Source:  "mcp",
			Payload: map[string]string{"triggered_by": agentID},
		}
		var result dispatch.DispatchResult
		var err error
		if args.Budget != "" {
			result, err = s.dispatcher.DispatchBudget(ctx, event, args.Agent, args.Priority, args.Budget)
		} else {
			result, err = s.dispatcher.Dispatch(ctx, event, args.Agent, args.Priority)
		}
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(result)
		return textResult(req.ID, string(data))

	case "sprint_status":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		items, err := s.sprintStore.GetAll(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		// Group by repo (replaces previous squad grouping — octi#271).
		grouped := make(map[string][]sprint.SprintItem)
		for _, item := range items {
			grouped[item.Repo] = append(grouped[item.Repo], item)
		}
		data, _ := json.Marshal(grouped)
		return textResult(req.ID, string(data))

	case "sprint_sync":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var synced []string
		for _, repo := range sprint.DefaultRepos {
			if err := s.sprintStore.Sync(ctx, repo); err != nil {
				synced = append(synced, fmt.Sprintf("%s: error: %v", repo, err))
			} else {
				synced = append(synced, fmt.Sprintf("%s: synced", repo))
			}
		}
		return textResult(req.ID, strings.Join(synced, "\n"))

	case "sprint_create":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo              string `json:"repo"`
			IssueNum          int    `json:"issue_num"`
			Title             string `json:"title"`
			Priority          int    `json:"priority"`
			DependsOn         []int  `json:"depends_on"`
			AssignTo          string `json:"assign_to"`
			CreateGitHubIssue bool   `json:"create_github_issue"`
			Body              string `json:"body"`
			Labels            string `json:"labels"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.CreateGitHubIssue && args.Repo != "" && args.IssueNum == 0 {
			num, err := sprint.CreateIssue(ctx, args.Repo, args.Title, args.Body, args.Labels)
			if err != nil {
				return errorResp(req.ID, -32000, fmt.Sprintf("create GitHub issue: %v", err))
			}
			args.IssueNum = num
		}
		item := sprint.SprintItem{
			Repo:      args.Repo,
			IssueNum:  args.IssueNum,
			Title:     args.Title,
			Priority:  args.Priority,
			DependsOn: args.DependsOn,
			AssignTo:  args.AssignTo,
		}
		if err := s.sprintStore.Create(ctx, item); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Sprint item created: %s#%d — %s (priority: %d)", args.Repo, args.IssueNum, args.Title, args.Priority))

	case "sprint_reprioritize":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo     string `json:"repo"`
			IssueNum int    `json:"issue_num"`
			Priority int    `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.IssueNum == 0 {
			return errorResp(req.ID, -32602, "issue_num is required")
		}
		if args.Repo == "" {
			found := false
			for _, repo := range sprint.DefaultRepos {
				if err := s.sprintStore.Reprioritize(ctx, repo, args.IssueNum, args.Priority); err == nil {
					args.Repo = repo
					found = true
					break
				}
			}
			if !found {
				return errorResp(req.ID, -32000, fmt.Sprintf("issue #%d not found in any sprint repo", args.IssueNum))
			}
		} else {
			if err := s.sprintStore.Reprioritize(ctx, args.Repo, args.IssueNum, args.Priority); err != nil {
				return errorResp(req.ID, -32000, err.Error())
			}
		}
		priorityLabel := [3]string{"P0 (critical)", "P1 (high)", "P2 (normal)"}
		label := "custom"
		if args.Priority >= 0 && args.Priority <= 2 {
			label = priorityLabel[args.Priority]
		}
		return textResult(req.ID, fmt.Sprintf("%s#%d reprioritized to %s", args.Repo, args.IssueNum, label))

	case "sprint_complete":
		if s.sprintStore == nil {
			return errorResp(req.ID, -32000, "sprint store not initialized")
		}
		var args struct {
			Repo      string `json:"repo"`
			IssueNum  int    `json:"issue_num"`
			Summary   string `json:"summary"`
			SkipGates bool   `json:"skip_gates"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.IssueNum == 0 {
			return errorResp(req.ID, -32602, "issue_num is required")
		}
		allItems, _ := s.sprintStore.GetAll(ctx)
		resolveItem := func(repo string) (bool, int) {
			for _, it := range allItems {
				if it.Repo == repo && it.IssueNum == args.IssueNum {
					return true, it.PRNumber
				}
			}
			return false, 0
		}
		runGates := func(repo string, prNumber int) (Response, bool) {
			if args.SkipGates {
				return Response{}, false
			}
			failed, err := s.runSprintCompleteGates(ctx, repo, prNumber)
			if err != nil {
				return errorResp(req.ID, -32000,
					fmt.Sprintf("sprint_complete blocked: gate %s failed: %v", failed, err)), true
			}
			return Response{}, false
		}
		if args.Repo == "" {
			for _, repo := range sprint.DefaultRepos {
				found, prNumber := resolveItem(repo)
				if !found {
					continue
				}
				if resp, block := runGates(repo, prNumber); block {
					return resp
				}
				unblocked, err := s.sprintStore.Complete(ctx, repo, args.IssueNum)
				if err == nil {
					args.Repo = repo
					msg := fmt.Sprintf("%s#%d marked done", repo, args.IssueNum)
					if len(unblocked) > 0 {
						nums := make([]string, len(unblocked))
						for i, n := range unblocked {
							nums[i] = fmt.Sprintf("#%d", n)
						}
						msg += fmt.Sprintf("; unblocked: %s", strings.Join(nums, ", "))
					}
					if args.Summary != "" {
						if err := sprint.CloseIssue(ctx, repo, args.IssueNum, args.Summary); err != nil {
							msg += fmt.Sprintf("; warning: could not close GitHub issue: %v", err)
						} else {
							msg += "; GitHub issue closed"
						}
					}
					return textResult(req.ID, msg)
				}
			}
			return errorResp(req.ID, -32000, fmt.Sprintf("issue #%d not found in any sprint repo", args.IssueNum))
		}
		found, prNumber := resolveItem(args.Repo)
		if !found {
			return errorResp(req.ID, -32000,
				fmt.Sprintf("issue #%d not found in sprint repo %s", args.IssueNum, args.Repo))
		}
		if resp, block := runGates(args.Repo, prNumber); block {
			return resp
		}
		unblocked, err := s.sprintStore.Complete(ctx, args.Repo, args.IssueNum)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("%s#%d marked done", args.Repo, args.IssueNum)
		if len(unblocked) > 0 {
			nums := make([]string, len(unblocked))
			for i, n := range unblocked {
				nums[i] = fmt.Sprintf("#%d", n)
			}
			msg += fmt.Sprintf("; unblocked: %s", strings.Join(nums, ", "))
		}
		if args.Summary != "" {
			if err := sprint.CloseIssue(ctx, args.Repo, args.IssueNum, args.Summary); err != nil {
				msg += fmt.Sprintf("; warning: could not close GitHub issue: %v", err)
			} else {
				msg += "; GitHub issue closed"
			}
		}
		return textResult(req.ID, msg)

	case "benchmark_status":
		if s.benchmark == nil {
			return errorResp(req.ID, -32000, "benchmark tracker not initialized")
		}
		metrics, err := s.benchmark.Compute(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(metrics)
		return textResult(req.ID, string(data))

	case "agent_leaderboard":
		if s.profiles == nil {
			return errorResp(req.ID, -32000, "profile store not initialized")
		}
		entries, err := s.profiles.Leaderboard(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, dispatch.FormatLeaderboard(entries))

	case "bootcheck_status":
		if s.bootcheckCache == nil {
			return errorResp(req.ID, -32000, "bootcheck cache not initialized")
		}
		rep := s.bootcheckCache.Get()
		if rep == nil {
			return textResult(req.ID, "no bootcheck report available yet")
		}
		data, _ := json.Marshal(rep)
		return textResult(req.ID, string(data))

	case "circuit_reset":
		var args struct {
			Driver string `json:"driver"`
			Note   string `json:"note"`
			Scope  string `json:"scope"`
		}
		json.Unmarshal(params.Arguments, &args)

		if args.Scope == "swarm" {
			if s.dispatcher == nil {
				return errorResp(req.ID, -32000, "dispatcher not initialized")
			}
			sc := s.dispatcher.SwarmCircuit()
			if sc == nil {
				return errorResp(req.ID, -32000, "swarm circuit subscriber not wired")
			}
			prev := sc.Snapshot()
			sc.Reset(args.Note)
			msg := fmt.Sprintf("circuit_reset(swarm): paused=%v→false (was signal=%s)", prev.Paused, prev.Signal)
			data, _ := json.Marshal(sc.Snapshot())
			return textResult(req.ID, msg+"\n"+string(data))
		}

		if args.Driver == "" {
			return errorResp(req.ID, -32602, "driver is required (or set scope=\"swarm\")")
		}
		prev := routing.DriverHealth{Name: args.Driver}
		for _, h := range s.router.HealthReport() {
			if h.Name == args.Driver {
				prev = h
				break
			}
		}
		newState, err := s.router.ForceClose(args.Driver)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		msg := fmt.Sprintf("circuit_reset: %s %s→CLOSED (failures %d→0)",
			args.Driver, prev.CircuitState, prev.Failures)
		if args.Note != "" {
			msg += " — " + args.Note
		}
		data, _ := json.Marshal(newState)
		return textResult(req.ID, msg+"\n"+string(data))

	case "budget_status":
		if s.budgetStore == nil {
			return errorResp(req.ID, -32000, "budget store not initialized")
		}
		var args struct {
			Agent string `json:"agent"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent != "" {
			b, err := s.budgetStore.GetBudget(ctx, args.Agent)
			if err != nil {
				return errorResp(req.ID, -32000, err.Error())
			}
			data, _ := json.Marshal(b)
			return textResult(req.ID, string(data))
		}
		all, err := s.budgetStore.ListAll(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(all) == 0 {
			return textResult(req.ID, "No budget records found.")
		}
		data, _ := json.Marshal(all)
		return textResult(req.ID, string(data))

	case "budget_set":
		if s.budgetStore == nil {
			return errorResp(req.ID, -32000, "budget store not initialized")
		}
		var args struct {
			Agent              string `json:"agent"`
			BudgetMonthlyCents int    `json:"budget_monthly_cents"`
			Driver             string `json:"driver"`
			Box                string `json:"box"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent == "" {
			return errorResp(req.ID, -32602, "agent is required")
		}
		result, err := s.budgetStore.UpsertBudget(ctx, budget.AgentBudget{
			Agent:              args.Agent,
			Driver:             args.Driver,
			Box:                args.Box,
			BudgetMonthlyCents: args.BudgetMonthlyCents,
		})
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		data, _ := json.Marshal(result)
		return textResult(req.ID, fmt.Sprintf("Budget set for %s: $%.2f/month\n%s",
			args.Agent, float64(result.BudgetMonthlyCents)/100.0, string(data)))

	case "budget_reset":
		if s.budgetStore == nil {
			return errorResp(req.ID, -32000, "budget store not initialized")
		}
		var args struct {
			Agent string `json:"agent"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Agent == "" {
			return errorResp(req.ID, -32602, "agent is required")
		}
		if err := s.budgetStore.MonthlyReset(ctx, args.Agent); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("Budget reset for %s: spent=0, runs=0, paused=false", args.Agent))

	case "admit_task":
		if s.admissionGate == nil {
			return errorResp(req.ID, -32000, "admission gate not initialized")
		}
		var args admission.TaskSpec
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResp(req.ID, -32602, "invalid arguments: "+err.Error())
		}
		score := s.admissionGate.Score(ctx, args)
		data, _ := json.Marshal(score)
		return textResult(req.ID, string(data))

	case "score_spec":
		var args admission.ArchitectSpec
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResp(req.ID, -32602, "invalid arguments: "+err.Error())
		}
		result := admission.ScoreSpec(args)
		data, _ := json.Marshal(result)
		return textResult(req.ID, string(data))

	case "lock_domain":
		if s.admissionGate == nil {
			return errorResp(req.ID, -32000, "admission gate not initialized")
		}
		var args struct {
			Domain     string `json:"domain"`
			Holder     string `json:"holder"`
			TTLSeconds int    `json:"ttl_seconds"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResp(req.ID, -32602, "invalid arguments: "+err.Error())
		}
		ttl := time.Duration(args.TTLSeconds) * time.Second
		if ttl <= 0 {
			ttl = 900 * time.Second
		}
		lock, err := s.admissionGate.AcquireLock(ctx, args.Domain, args.Holder, ttl)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if lock == nil {
			existing, _ := s.admissionGate.GetLock(ctx, args.Domain)
			if existing != nil {
				data, _ := json.Marshal(existing)
				return textResult(req.ID, fmt.Sprintf("DENIED: domain locked by %s (since %s)\n%s", existing.Holder, existing.AcquiredAt, data))
			}
			return textResult(req.ID, "DENIED: domain already locked")
		}
		data, _ := json.Marshal(lock)
		return textResult(req.ID, fmt.Sprintf("ACQUIRED: %s\n%s", args.Domain, data))

	case "unlock_domain":
		if s.admissionGate == nil {
			return errorResp(req.ID, -32000, "admission gate not initialized")
		}
		var args struct {
			Domain string `json:"domain"`
			Holder string `json:"holder"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResp(req.ID, -32602, "invalid arguments: "+err.Error())
		}
		if err := s.admissionGate.ReleaseLock(ctx, args.Domain, args.Holder); err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		return textResult(req.ID, fmt.Sprintf("RELEASED: %s", args.Domain))

	case "list_domain_locks":
		if s.admissionGate == nil {
			return errorResp(req.ID, -32000, "admission gate not initialized")
		}
		locks, err := s.admissionGate.ActiveLocks(ctx)
		if err != nil {
			return errorResp(req.ID, -32000, err.Error())
		}
		if len(locks) == 0 {
			return textResult(req.ID, "No active domain locks.")
		}
		data, _ := json.Marshal(locks)
		return textResult(req.ID, string(data))

	case "dispatch_anthropic":
		var args struct {
			Prompt   string `json:"prompt"`
			Repo     string `json:"repo"`
			Type     string `json:"type"`
			Priority string `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Type == "" {
			args.Type = "code-gen"
		}
		if args.Priority == "" {
			args.Priority = "normal"
		}
		if s.anthropicAdapter == nil {
			return errorResp(req.ID, -1, "anthropic adapter not configured")
		}
		task := &dispatch.Task{
			ID:       fmt.Sprintf("api_%d", time.Now().UnixMilli()),
			Type:     args.Type,
			Repo:     args.Repo,
			Prompt:   args.Prompt,
			Priority: args.Priority,
		}
		adResult, adErr := s.anthropicAdapter.Dispatch(ctx, task)
		if adErr != nil {
			return errorResp(req.ID, -1, adErr.Error())
		}
		b, _ := json.Marshal(adResult)
		return textResult(req.ID, string(b))

	case "dispatch_ghactions":
		var args struct {
			Prompt   string `json:"prompt"`
			Repo     string `json:"repo"`
			Type     string `json:"type"`
			Priority string `json:"priority"`
		}
		json.Unmarshal(params.Arguments, &args)
		if args.Type == "" {
			args.Type = "code-gen"
		}
		if args.Priority == "" {
			args.Priority = "normal"
		}
		if args.Repo == "" {
			return errorResp(req.ID, -1, "repo is required for gh-actions dispatch")
		}
		if s.ghActionsAdapter == nil {
			return errorResp(req.ID, -1, "gh-actions adapter not configured")
		}
		task := &dispatch.Task{
			ID:       fmt.Sprintf("gha_%d", time.Now().UnixMilli()),
			Type:     args.Type,
			Repo:     args.Repo,
			Prompt:   args.Prompt,
			Priority: args.Priority,
		}
		adResult, adErr := s.ghActionsAdapter.Dispatch(ctx, task)
		if adErr != nil {
			return errorResp(req.ID, -1, adErr.Error())
		}
		b, _ := json.Marshal(adResult)
		return textResult(req.ID, string(b))

	case "dispatch_prompt_to_cli":
		var args struct {
			Prompt          string `json:"prompt"`
			PreferredDriver string `json:"preferred_driver"`
			TimeoutSeconds  int    `json:"timeout_seconds"`
			SystemPrompt    string `json:"system_prompt"`
		}
		if err := json.Unmarshal(params.Arguments, &args); err != nil {
			return errorResp(req.ID, -32602, fmt.Sprintf("invalid args: %v", err))
		}
		if args.Prompt == "" {
			return errorResp(req.ID, -32602, "prompt is required")
		}
		if err := dispatch.ValidatePreferredDriver(args.PreferredDriver); err != nil {
			return errorResp(req.ID, -32602, err.Error())
		}
		adapter := s.promptCLIAdapter
		if adapter == nil {
			adapter = dispatch.NewPromptCLIAdapter()
		}
		timeout := time.Duration(args.TimeoutSeconds) * time.Second
		res := adapter.Dispatch(ctx, &dispatch.PromptCLIRequest{
			Prompt:          args.Prompt,
			SystemPrompt:    args.SystemPrompt,
			PreferredDriver: args.PreferredDriver,
			Timeout:         timeout,
		})
		b, _ := json.Marshal(res)
		return textResult(req.ID, string(b))

	default:
		return errorResp(req.ID, -32601, fmt.Sprintf("unknown tool: %s", params.Name))
	}
}

// EnrichedHealthEntry extends DriverHealth with derived observability fields.
type EnrichedHealthEntry struct {
	Name                 string `json:"name"`
	CircuitState         string `json:"circuit_state"`
	Failures             int    `json:"failures"`
	LastFailure          string `json:"last_failure,omitempty"`
	LastSuccess          string `json:"last_success,omitempty"`
	SecsSinceSuccess     int64  `json:"secs_since_last_success,omitempty"`
	DaysSinceLastSuccess int    `json:"days_since_last_success"`
	Stale                bool   `json:"stale"`
	Recommendation       string `json:"recommendation"`
}

// enrichHealthReport adds derived fields to each DriverHealth entry.
func enrichHealthReport(drivers []routing.DriverHealth) []EnrichedHealthEntry {
	now := time.Now().UTC()
	entries := make([]EnrichedHealthEntry, 0, len(drivers))
	for _, d := range drivers {
		e := EnrichedHealthEntry{
			Name:         d.Name,
			CircuitState: d.CircuitState,
			Failures:     d.Failures,
			LastFailure:  d.LastFailure,
			LastSuccess:  d.LastSuccess,
		}

		if d.LastSuccess != "" {
			if t, err := time.Parse(time.RFC3339, d.LastSuccess); err == nil {
				e.SecsSinceSuccess = int64(now.Sub(t).Seconds())
			}
		}
		e.DaysSinceLastSuccess = d.DaysSinceLastSuccess
		e.Stale = (d.DaysSinceLastSuccess >= 2) || (d.LastSuccess == "" && d.DaysSinceLastSuccess == -1)

		switch {
		case e.Stale && d.CircuitState != "OPEN":
			if d.LastSuccess == "" {
				e.Recommendation = fmt.Sprintf("%s: stale — no recorded success, investigate or remove", d.Name)
			} else {
				e.Recommendation = fmt.Sprintf("%s: stale — last success %dd ago, investigate or remove", d.Name, d.DaysSinceLastSuccess)
			}
		case d.CircuitState == "OPEN":
			e.Recommendation = fmt.Sprintf("%s: budget exhausted or unreachable — check quota and reset circuit", d.Name)
		case d.CircuitState == "HALF":
			e.Recommendation = fmt.Sprintf("%s: recovering — use with caution, monitor next run", d.Name)
		default:
			if e.SecsSinceSuccess > 3600 {
				e.Recommendation = fmt.Sprintf("%s: healthy but no success in %dh — verify driver is reachable", d.Name, e.SecsSinceSuccess/3600)
			} else {
				e.Recommendation = fmt.Sprintf("%s: healthy", d.Name)
			}
		}

		entries = append(entries, e)
	}
	return entries
}

// enrichHealthReport adds Redis-backed budget data and recommended actions.
func (s *Server) enrichHealthReport(ctx context.Context, drivers []routing.DriverHealth) []routing.DriverHealth {
	for i, h := range drivers {
		if s.rdb != nil {
			budgetKey := s.redisNS + ":driver-budget:" + h.Name
			vals, err := s.rdb.HGetAll(ctx, budgetKey).Result()
			if err == nil && len(vals) > 0 {
				if pctStr, ok := vals["pct"]; ok {
					if pct, err := strconv.Atoi(pctStr); err == nil {
						drivers[i].BudgetPct = &pct
					}
				}
			}
		}
		drivers[i].RecommendedAction = routing.RecommendAction(drivers[i])
	}
	return drivers
}

func textResult(id interface{}, text string) Response {
	return Response{
		JSONRPC: "2.0",
		ID:      id,
		Result: map[string]interface{}{
			"content": []map[string]string{{"type": "text", "text": text}},
		},
	}
}

func errorResp(id interface{}, code int, msg string) Response {
	return Response{JSONRPC: "2.0", ID: id, Error: &RPCError{Code: code, Message: msg}}
}

func toolDefs() []ToolDef {
	return []ToolDef{
		{
			Name:        "memory_store",
			Description: "Store a learning in the swarm knowledge base, tagged with your identity and topics.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"content": map[string]string{"type": "string", "description": "What you learned / observed / decided"},
					"topics":  map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Topic tags"},
				},
				"required": []string{"content", "topics"},
			},
		},
		{
			Name:        "memory_recall",
			Description: "Search the swarm knowledge base by keyword and semantic similarity.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"query": map[string]string{"type": "string", "description": "What are you looking for?"},
					"limit": map[string]interface{}{"type": "number", "description": "Max results (default 5)"},
				},
				"required": []string{"query"},
			},
		},
		{
			Name:        "memory_status",
			Description: "See what other agents in the swarm are currently working on.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "tier_activity",
			Description: "Summarize dispatch activity by Ladder Forge tier over the last N hours.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"windowHours": map[string]interface{}{"type": "integer", "description": "Lookback window in hours (default 24)."},
					"limit":       map[string]interface{}{"type": "integer", "description": "Max log entries to scan (default 500)."},
				},
			},
		},
		{
			Name:        "coord_claim",
			Description: "Claim a task so no other agent duplicates your work. Auto-expires.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"task":       map[string]string{"type": "string", "description": "What you are working on"},
					"ttlSeconds": map[string]interface{}{"type": "number", "description": "Claim duration in seconds (default 900)"},
				},
				"required": []string{"task"},
			},
		},
		{
			Name:        "coord_signal",
			Description: "Broadcast a signal to the swarm — completion, blocker, or need-help.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"type":    map[string]interface{}{"type": "string", "enum": []string{"completed", "blocked", "need-help", "directive"}, "description": "Signal type"},
					"payload": map[string]string{"type": "string", "description": "Details"},
				},
				"required": []string{"type", "payload"},
			},
		},
		{
			Name:        "route_recommend",
			Description: "Get the recommended driver for a task based on cost tier and driver health.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"taskDescription": map[string]string{"type": "string", "description": "Describe the task"},
					"budget":          map[string]interface{}{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "Budget tier"},
				},
				"required": []string{"taskDescription"},
			},
		},
		{
			Name:        "health_report",
			Description: "Get current health status of all drivers in the swarm.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "dispatch_event",
			Description: "Submit an event for routing through the dispatcher.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"eventType": map[string]interface{}{"type": "string", "description": "Event type"},
					"source":    map[string]string{"type": "string", "description": "Event source"},
					"repo":      map[string]string{"type": "string", "description": "Repository full name"},
					"payload":   map[string]interface{}{"type": "object", "description": "Event-specific key-value data"},
					"priority":  map[string]interface{}{"type": "number", "description": "Priority (0=critical, 3=background)"},
				},
				"required": []string{"eventType"},
			},
		},
		{
			Name:        "dispatch_status",
			Description: "Show current dispatch queue depth, pending agents, and recent dispatch decisions.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "dispatch_trigger",
			Description: "Manually trigger an agent run. Bypasses event matching but still respects cooldown.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent":    map[string]string{"type": "string", "description": "Agent name to trigger"},
					"priority": map[string]interface{}{"type": "number", "description": "Priority (0=critical, 3=background). Default: 1"},
					"budget":   map[string]interface{}{"type": "string", "enum": []string{"low", "medium", "high"}, "description": "Budget tier override"},
				},
				"required": []string{"agent"},
			},
		},
		{
			Name:        "sprint_status",
			Description: "Return all sprint items grouped by repo. Shows issue numbers, titles, priority, status, and dependencies.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "sprint_sync",
			Description: "Trigger a sync of sprint items from GitHub issues across all tracked repos.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "sprint_create",
			Description: "Manually create or upsert a sprint item.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"repo":                map[string]string{"type": "string", "description": "Repo (e.g. chitinhq/octi)"},
					"issue_num":           map[string]interface{}{"type": "number", "description": "GitHub issue number."},
					"title":               map[string]string{"type": "string", "description": "Sprint item title"},
					"priority":            map[string]interface{}{"type": "number", "description": "Priority: 0=P0, 1=P1, 2=P2"},
					"depends_on":          map[string]interface{}{"type": "array", "items": map[string]string{"type": "number"}, "description": "Issue numbers that must complete first"},
					"assign_to":           map[string]string{"type": "string", "description": "Agent name to assign."},
					"create_github_issue": map[string]interface{}{"type": "boolean", "description": "When true, creates a real GitHub issue."},
					"body":                map[string]string{"type": "string", "description": "Issue body."},
					"labels":              map[string]string{"type": "string", "description": "Comma-separated label names."},
				},
				"required": []string{"repo", "title"},
			},
		},
		{
			Name:        "sprint_reprioritize",
			Description: "Change the priority of a sprint item.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"issue_num": map[string]interface{}{"type": "number", "description": "Issue number"},
					"priority":  map[string]interface{}{"type": "number", "description": "New priority: 0=P0, 1=P1, 2=P2"},
					"repo":      map[string]string{"type": "string", "description": "Repo"},
				},
				"required": []string{"issue_num", "priority"},
			},
		},
		{
			Name:        "sprint_complete",
			Description: "Mark a sprint item as done and optionally close the GitHub issue.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"issue_num":  map[string]interface{}{"type": "number", "description": "Issue number"},
					"repo":       map[string]string{"type": "string", "description": "Repo"},
					"summary":    map[string]string{"type": "string", "description": "Run summary."},
					"skip_gates": map[string]string{"type": "boolean", "description": "Migration escape hatch."},
				},
				"required": []string{"issue_num"},
			},
		},
		{
			Name:        "benchmark_status",
			Description: "Return swarm throughput and health metrics.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "agent_leaderboard",
			Description: "Rank all agents by productivity score.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "bootcheck_status",
			Description: "Return the most recent startup telemetry self-audit report.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "circuit_reset",
			Description: "Manually reset a circuit breaker.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"scope":  map[string]interface{}{"type": "string", "enum": []string{"driver", "swarm"}, "description": "Which circuit to reset."},
					"driver": map[string]string{"type": "string", "description": "Driver name."},
					"note":   map[string]string{"type": "string", "description": "Optional reason for the manual reset."},
				},
			},
		},
		{
			Name:        "budget_status",
			Description: "View budget for a specific agent or all agents.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]string{"type": "string", "description": "Agent name. Omit to list all."},
				},
			},
		},
		{
			Name:        "budget_set",
			Description: "Provision or update an agent's monthly budget.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent":                map[string]string{"type": "string", "description": "Agent name"},
					"budget_monthly_cents": map[string]interface{}{"type": "number", "description": "Monthly budget limit in cents"},
					"driver":               map[string]string{"type": "string", "description": "Driver name."},
					"box":                  map[string]string{"type": "string", "description": "Box/host."},
				},
				"required": []string{"agent", "budget_monthly_cents"},
			},
		},
		{
			Name:        "budget_reset",
			Description: "Monthly reset for an agent.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"agent": map[string]string{"type": "string", "description": "Agent name to reset"},
				},
				"required": []string{"agent"},
			},
		},
		{
			Name:        "admit_task",
			Description: "Score a candidate task for admission to the swarm.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":            map[string]string{"type": "string", "description": "Short task title"},
					"repo":             map[string]string{"type": "string", "description": "Target repo"},
					"file_paths":       map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Files the task will touch"},
					"priority":         map[string]interface{}{"type": "integer", "description": "0=CRITICAL, 3=BACKGROUND"},
					"is_reversible":    map[string]interface{}{"type": "boolean", "description": "Whether the changes can be easily undone"},
					"spec_clarity":     map[string]interface{}{"type": "number", "description": "0.0-1.0: how complete the spec is"},
					"estimated_tokens": map[string]interface{}{"type": "integer", "description": "Approximate token cost"},
				},
				"required": []string{"title", "repo"},
			},
		},
		{
			Name:        "score_spec",
			Description: "Score an architect-stage spec for completeness.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"title":                 map[string]string{"type": "string", "description": "Original issue/task title"},
					"acceptance_criteria":   map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Conditions for completion"},
					"files_touched":         map[string]interface{}{"type": "array", "items": map[string]string{"type": "string"}, "description": "Files to modify"},
					"blast_radius_estimate": map[string]interface{}{"type": "integer", "description": "Total files changed estimate"},
					"approach":              map[string]string{"type": "string", "description": "Specific implementation strategy"},
				},
				"required": []string{"title"},
			},
		},
		{
			Name:        "lock_domain",
			Description: "Acquire an exclusive domain lock before touching a contested surface.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain":      map[string]string{"type": "string", "description": "Lock target"},
					"holder":      map[string]string{"type": "string", "description": "Agent identity"},
					"ttl_seconds": map[string]interface{}{"type": "integer", "description": "Lock expiry in seconds."},
				},
				"required": []string{"domain", "holder"},
			},
		},
		{
			Name:        "unlock_domain",
			Description: "Release a domain lock.",
			InputSchema: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"domain": map[string]string{"type": "string", "description": "Domain surface"},
					"holder": map[string]string{"type": "string", "description": "Agent identity"},
				},
				"required": []string{"domain", "holder"},
			},
		},
		{
			Name:        "list_domain_locks",
			Description: "List all currently active domain locks across the swarm.",
			InputSchema: map[string]interface{}{"type": "object", "properties": map[string]interface{}{}},
		},
		{
			Name:        "dispatch_anthropic",
			Description: "Dispatch a task to the Anthropic API via ShellForge agent loop",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":   map[string]any{"type": "string", "description": "Task prompt"},
					"repo":     map[string]any{"type": "string", "description": "Target repo"},
					"type":     map[string]any{"type": "string", "description": "Task type"},
					"priority": map[string]any{"type": "string", "description": "Priority"},
				},
				"required": []string{"prompt"},
			},
		},
		{
			Name:        "dispatch_prompt_to_cli",
			Description: "Dispatch a freeform prompt to a local CLI agent (openclaw).",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":           map[string]any{"type": "string", "description": "Freeform prompt"},
					"preferred_driver": map[string]any{"type": "string", "description": "openclaw (optional)"},
					"timeout_seconds":  map[string]any{"type": "integer", "description": "Timeout in seconds"},
					"system_prompt":    map[string]any{"type": "string", "description": "Optional system prompt"},
				},
				"required": []string{"prompt"},
			},
		},
		{
			Name:        "dispatch_ghactions",
			Description: "Dispatch a task to GitHub Actions Copilot Agent",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"prompt":   map[string]any{"type": "string", "description": "Task prompt"},
					"repo":     map[string]any{"type": "string", "description": "Target repo"},
					"type":     map[string]any{"type": "string", "description": "Task type"},
					"priority": map[string]any{"type": "string", "description": "Priority level"},
				},
				"required": []string{"prompt", "repo"},
			},
		},
		{
			Name:        "swarm_today",
			Description: "1-screen daily swarm observability view.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"window_hours": map[string]any{"type": "integer", "description": "Look-back window in hours (default 24)"},
				},
			},
		},
	}
}
