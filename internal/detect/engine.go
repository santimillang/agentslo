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
	// RatioWindow is the rolling window for ToolErrRate and LoopRate (default 1h).
	RatioWindow time.Duration
}

// Engine is the single entry point the collector processor uses.
type Engine struct {
	cfg   Config
	loops *LoopDetector
	burn  *BurnTracker

	mu     sync.Mutex
	agents map[string]*agentStats
	looped map[string]time.Time // session -> time of its last loop event
}

type agentStats struct {
	tools          *ring                   // tool calls in time buckets; good = no error
	sessions       map[string]*sessionSeen // sessions seen within RatioWindow
	tasks          int64
	tokens         int64
	baselineTokens float64 // EWMA of tokens per task
}

type sessionSeen struct {
	last   time.Time // end of the latest tool/task span in the session
	looped bool
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
	if cfg.RatioWindow <= 0 {
		cfg.RatioWindow = time.Hour
	}
	return &Engine{
		cfg:    cfg,
		loops:  NewLoopDetector(cfg.Loop),
		burn:   NewBurnTracker(cfg.SLO),
		agents: map[string]*agentStats{},
		looped: map[string]time.Time{},
	}
}

// bucket is the time resolution shared by the burn and tool-call rings.
func (e *Engine) bucket() time.Duration { return e.burn.cfg.Bucket }

func (e *Engine) stats(agent string) *agentStats {
	a, ok := e.agents[agent]
	if !ok {
		a = &agentStats{
			tools:    newRing(e.cfg.RatioWindow, e.bucket()),
			sessions: map[string]*sessionSeen{},
		}
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
		ss, ok := a.sessions[s.Session()]
		if !ok {
			ss = &sessionSeen{}
			a.sessions[s.Session()] = ss
		}
		if s.End.After(ss.last) {
			ss.last = s.End
		}
	}
	for _, ev := range events {
		e.looped[ev.SessionID] = ev.At
		if ss, ok := a.sessions[ev.SessionID]; ok {
			ss.looped = true
		}
	}

	switch s.Kind {
	case KindTool:
		a.tools.add(s.End.UnixNano()/int64(e.bucket()), !s.Error)
	case KindLLM:
		a.tokens += s.InputTokens + s.OutputTokens
	case KindAgent:
		a.tasks++
		_, looped := e.looped[s.Session()]
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
		if good, total := a.tools.sum(bucketRange(now, e.cfg.RatioWindow, e.bucket())); total > 0 {
			snap.ToolErrRate = float64(total-good) / float64(total)
		}
		cutoff := now.Add(-e.cfg.RatioWindow)
		var seen, looped int
		for _, ss := range a.sessions {
			if ss.last.Before(cutoff) {
				continue
			}
			seen++
			if ss.looped {
				looped++
			}
		}
		if seen > 0 {
			snap.LoopRate = float64(looped) / float64(seen)
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

// Evict drops state that has aged out: idle loop-detector sessions, and
// sessions and loop marks older than RatioWindow. It never changes the
// rolling ratios, which already ignore anything outside the window.
func (e *Engine) Evict(now time.Time) {
	e.loops.Evict(now)
	cutoff := now.Add(-e.cfg.RatioWindow)
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, a := range e.agents {
		for k, ss := range a.sessions {
			if ss.last.Before(cutoff) {
				delete(a.sessions, k)
			}
		}
	}
	for k, at := range e.looped {
		if at.Before(cutoff) {
			delete(e.looped, k)
		}
	}
}
