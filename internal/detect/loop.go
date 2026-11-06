package detect

import (
	"sync"
	"time"
)

// LoopKind distinguishes the failure patterns the detector reports.
type LoopKind string

const (
	// LoopRepeat: the same tool with the same arguments called Threshold times
	// within the window. The agent is grinding without making progress.
	LoopRepeat LoopKind = "repeat"
	// LoopRetryStorm: the same tool failing Threshold times within the window,
	// whatever the arguments. Usually a broken tool the agent keeps poking.
	LoopRetryStorm LoopKind = "retry_storm"
	// LoopOscillation: A,B,A,B,... between two distinct calls.
	LoopOscillation LoopKind = "oscillation"
)

// LoopEvent is emitted once per session and kind when a pattern trips.
type LoopEvent struct {
	Kind      LoopKind
	SessionID string
	Agent     string
	Tool      string
	Count     int
	At        time.Time
}

// LoopConfig tunes the detector.
type LoopConfig struct {
	Window    int           // last N tool calls kept per session (default 10)
	Threshold int           // occurrences that trip a pattern (default 4)
	IdleTTL   time.Duration // sessions untouched this long are evicted (default 30m)
}

func (c LoopConfig) withDefaults() LoopConfig {
	if c.Window <= 0 {
		c.Window = 10
	}
	if c.Threshold <= 0 {
		c.Threshold = 4
	}
	if c.IdleTTL <= 0 {
		c.IdleTTL = 30 * time.Minute
	}
	return c
}

type call struct {
	fp   string // tool + args hash
	tool string
	err  bool
}

type session struct {
	calls    []call
	lastSeen time.Time
	fired    map[LoopKind]bool
}

// LoopDetector tracks tool calls per session. It is safe for concurrent use.
type LoopDetector struct {
	cfg      LoopConfig
	mu       sync.Mutex
	sessions map[string]*session
}

func NewLoopDetector(cfg LoopConfig) *LoopDetector {
	return &LoopDetector{cfg: cfg.withDefaults(), sessions: map[string]*session{}}
}

// Observe feeds one span. Non-tool spans are ignored. It returns any patterns
// that tripped on this call; each kind fires at most once per session so a
// stuck agent produces one event, not one per call.
func (d *LoopDetector) Observe(s Span) []LoopEvent {
	if s.Kind != KindTool {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	key := s.Session()
	ss, ok := d.sessions[key]
	if !ok {
		ss = &session{fired: map[LoopKind]bool{}}
		d.sessions[key] = ss
	}
	ss.lastSeen = s.End
	ss.calls = append(ss.calls, call{fp: s.Tool + "\x00" + s.ArgsHash, tool: s.Tool, err: s.Error})
	if len(ss.calls) > d.cfg.Window {
		ss.calls = ss.calls[len(ss.calls)-d.cfg.Window:]
	}

	var out []LoopEvent
	emit := func(k LoopKind, n int) {
		if ss.fired[k] {
			return
		}
		ss.fired[k] = true
		out = append(out, LoopEvent{Kind: k, SessionID: key, Agent: s.Agent, Tool: s.Tool, Count: n, At: s.End})
	}

	cur := ss.calls[len(ss.calls)-1]
	same, fails := 0, 0
	for _, c := range ss.calls {
		if c.fp == cur.fp {
			same++
		}
		if c.tool == cur.tool && c.err {
			fails++
		}
	}
	// Repeat needs the arguments: without them every call to a tool looks
	// identical and normal fan-out ("search" x5 with different queries) would trip.
	if s.ArgsHash != "" && same >= d.cfg.Threshold {
		emit(LoopRepeat, same)
	}
	if cur.err && fails >= d.cfg.Threshold {
		emit(LoopRetryStorm, fails)
	}
	if n := oscillation(ss.calls); s.ArgsHash != "" && n >= 2*d.cfg.Threshold-1 {
		emit(LoopOscillation, n)
	}
	return out
}

// oscillation returns the length of the trailing A,B,A,B run (A != B).
func oscillation(calls []call) int {
	n := len(calls)
	if n < 2 || calls[n-1].fp == calls[n-2].fp {
		return 0
	}
	run := 2
	for i := n - 3; i >= 0; i-- {
		if calls[i].fp != calls[i+2].fp {
			break
		}
		run++
	}
	return run
}

// Evict drops sessions idle since before now-IdleTTL. Call it periodically.
func (d *LoopDetector) Evict(now time.Time) int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for k, s := range d.sessions {
		if now.Sub(s.lastSeen) > d.cfg.IdleTTL {
			delete(d.sessions, k)
			n++
		}
	}
	return n
}
