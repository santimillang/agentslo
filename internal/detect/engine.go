package detect

import (
	"sort"
	"sync"
	"time"
)

// Config bundles the detector settings.
type Config struct {
	Loop   LoopConfig
	SLO    BurnConfig
	Rules  []AlertRule
	Health HealthWeights
}

// Engine is the single entry point the collector processor uses.
type Engine struct {
	cfg   Config
	loops *LoopDetector
	burn  *BurnTracker

	mu     sync.Mutex
	agents map[string]*agentStats
	looped map[string]bool // session -> had a loop event
}

type agentStats struct {
	toolCalls, toolErrs int64
	sessions            map[string]struct{}
	loopSessions        map[string]struct{}
	tasks               int64
	tokens              int64
	baselineTokens      float64 // EWMA of tokens per task
}

// AgentSnapshot is what gets exported as metrics.
type AgentSnapshot struct {
	Agent       string
	BurnRate1h  float64
	BurnRate6h  float64
	ToolErrRate float64
	LoopRate    float64
	Health      float64
	Firing      []string
}

func NewEngine(cfg Config) *Engine {
	if cfg.Rules == nil {
		cfg.Rules = DefaultRules
	}
	if cfg.Health == (HealthWeights{}) {
		cfg.Health = DefaultHealthWeights
	}
	return &Engine{
		cfg:    cfg,
		loops:  NewLoopDetector(cfg.Loop),
		burn:   NewBurnTracker(cfg.SLO),
		agents: map[string]*agentStats{},
		looped: map[string]bool{},
	}
}

func (e *Engine) stats(agent string) *agentStats {
	a, ok := e.agents[agent]
	if !ok {
		a = &agentStats{sessions: map[string]struct{}{}, loopSessions: map[string]struct{}{}}
		e.agents[agent] = a
	}
	return a
}

// Observe feeds one span and returns loop events it caused. Feed spans in end
// order (the order a collector sees them): a task span arrives after its tool
// spans, so its loop status is known when it is scored.
func (e *Engine) Observe(s Span) []LoopEvent {
	events := e.loops.Observe(s)

	e.mu.Lock()
	defer e.mu.Unlock()
	a := e.stats(s.Agent)
	// Only tool and task spans define sessions: loop events are keyed on
	// them, while LLM spans often lack gen_ai.conversation.id and would fall
	// back to a trace id, inflating the LoopRate denominator.
	if s.Kind == KindTool || s.Kind == KindAgent {
		a.sessions[s.Session()] = struct{}{}
	}
	for _, ev := range events {
		e.looped[ev.SessionID] = true
		a.loopSessions[ev.SessionID] = struct{}{}
	}

	switch s.Kind {
	case KindTool:
		a.toolCalls++
		if s.Error {
			a.toolErrs++
		}
	case KindLLM:
		a.tokens += s.InputTokens + s.OutputTokens
	case KindAgent:
		a.tasks++
		looped := e.looped[s.Session()]
		e.burn.Record(s.Agent, s.End, e.burn.Good(s, looped))
		// Baseline tokens/task as a slow EWMA; cost ratio compares against it.
		perTask := float64(a.tokens)
		a.tokens = 0
		if a.baselineTokens == 0 {
			a.baselineTokens = perTask
		} else {
			a.baselineTokens = 0.95*a.baselineTokens + 0.05*perTask
		}
	}
	return events
}

// Snapshot computes current per-agent signals, sorted by agent name.
func (e *Engine) Snapshot(now time.Time) []AgentSnapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]AgentSnapshot, 0, len(e.agents))
	for name, a := range e.agents {
		snap := AgentSnapshot{Agent: name}
		snap.BurnRate1h, _ = e.burn.BurnRate(name, now, time.Hour)
		snap.BurnRate6h, _ = e.burn.BurnRate(name, now, 6*time.Hour)
		if a.toolCalls > 0 {
			snap.ToolErrRate = float64(a.toolErrs) / float64(a.toolCalls)
		}
		if n := len(a.sessions); n > 0 {
			snap.LoopRate = float64(len(a.loopSessions)) / float64(n)
		}
		snap.Health = Health(HealthInputs{
			BurnRate1h:  snap.BurnRate1h,
			ToolErrRate: snap.ToolErrRate,
			LoopRate:    snap.LoopRate,
		}, e.cfg.Health)
		for _, r := range e.burn.Firing(name, now, e.cfg.Rules) {
			snap.Firing = append(snap.Firing, r.Name)
		}
		out = append(out, snap)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Agent < out[j].Agent })
	return out
}

// Evict drops idle per-session state.
func (e *Engine) Evict(now time.Time) {
	e.loops.Evict(now)
	// Session sets in agentStats grow unbounded; reset them each eviction
	// cycle so LoopRate is "since last eviction". Good enough for v0.
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.agents {
		a.sessions = map[string]struct{}{}
		a.loopSessions = map[string]struct{}{}
		a.toolCalls, a.toolErrs = 0, 0
	}
	e.looped = map[string]bool{}
}
