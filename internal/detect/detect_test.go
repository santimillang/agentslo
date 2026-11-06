package detect

import (
	"testing"
	"time"
)

var t0 = time.Date(2026, 11, 6, 23, 0, 0, 0, time.UTC)

func tool(session, name, args string, err bool, at time.Time) Span {
	return Span{SessionID: session, Agent: "support-bot", Kind: KindTool, Tool: name, ArgsHash: args, Error: err, Start: at, End: at.Add(100 * time.Millisecond)}
}

func task(session string, err bool, at time.Time, d time.Duration) Span {
	return Span{SessionID: session, Agent: "support-bot", Kind: KindAgent, Error: err, Start: at, End: at.Add(d)}
}

func TestRepeatLoopFiresOnce(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Window: 10, Threshold: 4})
	var events []LoopEvent
	for i := 0; i < 8; i++ {
		events = append(events, d.Observe(tool("s1", "search", "h1", false, t0.Add(time.Duration(i)*time.Second)))...)
	}
	if len(events) != 1 || events[0].Kind != LoopRepeat || events[0].Count != 4 {
		t.Fatalf("want one repeat event at count 4, got %+v", events)
	}
}

func TestFanOutIsNotALoop(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Threshold: 4})
	for i, args := range []string{"a", "b", "c", "d", "e", "f"} {
		if ev := d.Observe(tool("s1", "search", args, false, t0.Add(time.Duration(i)*time.Second))); len(ev) > 0 {
			t.Fatalf("distinct args should not trip: %+v", ev)
		}
	}
}

func TestRetryStormIgnoresArgs(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Threshold: 3})
	var got []LoopEvent
	for i, args := range []string{"a", "b", "c"} {
		got = append(got, d.Observe(tool("s1", "db_query", args, true, t0.Add(time.Duration(i)*time.Second)))...)
	}
	if len(got) != 1 || got[0].Kind != LoopRetryStorm {
		t.Fatalf("want retry storm, got %+v", got)
	}
}

func TestOscillation(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Threshold: 3}) // needs a run of 5
	seq := []string{"a", "b", "a", "b", "a"}
	var got []LoopEvent
	for i, a := range seq {
		got = append(got, d.Observe(tool("s1", "nav", a, false, t0.Add(time.Duration(i)*time.Second)))...)
	}
	// "a" also appears 3 times, but every occurrence is inside the
	// alternating run, so it counts as oscillation only, not also repeat.
	if len(got) != 1 || got[0].Kind != LoopOscillation {
		t.Fatalf("want exactly one oscillation event, got %+v", got)
	}
}

// Repeats outside the alternating run are still a repeat loop.
func TestRepeatOutsideOscillationStillFires(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Threshold: 3})
	seq := []string{"a", "a", "b", "a"} // trailing run is just "b","a"
	var got []LoopEvent
	for i, a := range seq {
		got = append(got, d.Observe(tool("s1", "nav", a, false, t0.Add(time.Duration(i)*time.Second)))...)
	}
	if len(got) != 1 || got[0].Kind != LoopRepeat {
		t.Fatalf("want one repeat event, got %+v", got)
	}
}

func TestSessionsAreIsolated(t *testing.T) {
	d := NewLoopDetector(LoopConfig{Threshold: 4})
	for i := 0; i < 3; i++ {
		d.Observe(tool("s1", "search", "h", false, t0))
		if ev := d.Observe(tool("s2", "search", "h", false, t0)); len(ev) > 0 {
			t.Fatal("calls in different sessions must not combine")
		}
	}
}

func TestEvict(t *testing.T) {
	d := NewLoopDetector(LoopConfig{IdleTTL: time.Minute})
	d.Observe(tool("s1", "x", "h", false, t0))
	if n := d.Evict(t0.Add(2 * time.Minute)); n != 1 {
		t.Fatalf("want 1 evicted, got %d", n)
	}
}

func TestBurnRate(t *testing.T) {
	b := NewBurnTracker(BurnConfig{Objective: 0.99})
	// 100 tasks in the last 5 minutes, 10 bad => 10% errors => burn 10x.
	for i := 0; i < 100; i++ {
		b.Record("bot", t0.Add(-time.Duration(i)*time.Second), i >= 10)
	}
	rate, ok := b.BurnRate("bot", t0, 5*time.Minute)
	if !ok || rate < 9.99 || rate > 10.01 {
		t.Fatalf("want burn 10, got %v (ok=%v)", rate, ok)
	}
	if _, ok := b.BurnRate("nobody", t0, time.Hour); ok {
		t.Fatal("unknown agent should report no traffic")
	}
}

func TestBurnRingWrapsOldData(t *testing.T) {
	b := NewBurnTracker(BurnConfig{Objective: 0.99, Bucket: time.Minute, Retention: 10 * time.Minute})
	b.Record("bot", t0, false) // old, bad
	later := t0.Add(30 * time.Minute)
	b.Record("bot", later, true)
	if rate, _ := b.BurnRate("bot", later, 10*time.Minute); rate != 0 {
		t.Fatalf("stale bucket leaked into window: burn %v", rate)
	}
}

func TestPageAlertFiresOnFastBurn(t *testing.T) {
	b := NewBurnTracker(BurnConfig{Objective: 0.99})
	// 50% errors for the last hour: burn 50x on both windows.
	for i := 0; i < 600; i++ {
		b.Record("bot", t0.Add(-time.Duration(i)*6*time.Second), i%2 == 0)
	}
	f := b.Firing("bot", t0, DefaultRules)
	if len(f) == 0 || f[0].Name != "page" {
		t.Fatalf("want page alert, got %+v", f)
	}
}

func TestHealthBounds(t *testing.T) {
	if h := Health(HealthInputs{}, DefaultHealthWeights); h != 100 {
		t.Fatalf("clean agent should be 100, got %v", h)
	}
	worst := Health(HealthInputs{BurnRate1h: 100, ToolErrRate: 1, LoopRate: 1, CostRatio: 10}, DefaultHealthWeights)
	if worst != 0 {
		t.Fatalf("everything on fire should be 0, got %v", worst)
	}
}

func TestEngineLoopMakesTaskBad(t *testing.T) {
	e := NewEngine(Config{Loop: LoopConfig{Threshold: 3}, SLO: BurnConfig{Objective: 0.9}})
	// Healthy session.
	e.Observe(tool("ok", "search", "q", false, t0))
	e.Observe(task("ok", false, t0, time.Second))
	// Looping session: task itself "succeeds" but burned budget grinding.
	for i := 0; i < 3; i++ {
		e.Observe(tool("loop", "search", "same", false, t0.Add(time.Duration(i)*time.Second)))
	}
	e.Observe(task("loop", false, t0, 5*time.Second))

	snaps := e.Snapshot(t0.Add(10 * time.Second))
	if len(snaps) != 1 {
		t.Fatalf("want one agent, got %d", len(snaps))
	}
	s := snaps[0]
	// 1 of 2 tasks bad => 50% errors / 10% budget = burn 5.
	if s.BurnRate1h < 4.99 || s.BurnRate1h > 5.01 {
		t.Fatalf("want burn 5, got %v", s.BurnRate1h)
	}
	if s.LoopRate != 0.5 {
		t.Fatalf("want loop rate 0.5, got %v", s.LoopRate)
	}
	if s.Health >= 100 || s.Health <= 0 {
		t.Fatalf("health should be degraded but not zero, got %v", s.Health)
	}
}

// LLM spans often carry no conversation id, so Session() falls back to the
// trace id. They must not count as extra sessions in LoopRate.
func TestLoopRateIgnoresLLMSessionFallback(t *testing.T) {
	e := NewEngine(Config{Loop: LoopConfig{Threshold: 3}, SLO: BurnConfig{Objective: 0.9}})
	llm := func(trace string) Span {
		return Span{TraceID: trace, Agent: "support-bot", Kind: KindLLM, Start: t0, End: t0, InputTokens: 10}
	}
	e.Observe(llm("trace-ok"))
	e.Observe(tool("ok", "search", "q", false, t0))
	e.Observe(task("ok", false, t0, time.Second))
	for i := 0; i < 3; i++ {
		e.Observe(llm("trace-loop"))
		e.Observe(tool("loop", "search", "same", false, t0.Add(time.Duration(i)*time.Second)))
	}
	e.Observe(task("loop", false, t0, 5*time.Second))

	if got := e.Snapshot(t0.Add(10 * time.Second))[0].LoopRate; got != 0.5 {
		t.Fatalf("want loop rate 0.5 (1 of 2 sessions), got %v", got)
	}
}

// Eviction must only drop idle state, never reset the ratios: they are
// rolling-window values, not "since last eviction".
func TestRatiosSurviveEvict(t *testing.T) {
	e := NewEngine(Config{Loop: LoopConfig{Threshold: 3, IdleTTL: time.Minute}, RatioWindow: time.Hour})
	e.Observe(tool("ok", "search", "q", true, t0)) // 1 failed call
	for i := 0; i < 3; i++ {
		e.Observe(tool("loop", "search", "same", false, t0.Add(time.Duration(i)*time.Second)))
	}
	now := t0.Add(5 * time.Minute)
	before := e.Snapshot(now)[0]
	e.Evict(now)
	after := e.Snapshot(now)[0]
	if after.ToolErrRate != before.ToolErrRate || after.LoopRate != before.LoopRate {
		t.Fatalf("evict changed ratios: before err=%v loop=%v, after err=%v loop=%v",
			before.ToolErrRate, before.LoopRate, after.ToolErrRate, after.LoopRate)
	}
	if after.ToolErrRate != 0.25 || after.LoopRate != 0.5 {
		t.Fatalf("want err 0.25 loop 0.5, got %v %v", after.ToolErrRate, after.LoopRate)
	}
}

// Ratios only cover RatioWindow: old traffic ages out with or without Evict.
func TestRatiosAgeOutOfWindow(t *testing.T) {
	e := NewEngine(Config{Loop: LoopConfig{Threshold: 3}, RatioWindow: 10 * time.Minute})
	for i := 0; i < 3; i++ {
		e.Observe(tool("old", "search", "same", true, t0.Add(time.Duration(i)*time.Second)))
	}
	e.Observe(tool("new", "search", "q", false, t0.Add(30*time.Minute)))
	now := t0.Add(31 * time.Minute)
	for _, evict := range []bool{false, true} {
		if evict {
			e.Evict(now)
		}
		s := e.Snapshot(now)[0]
		if s.ToolErrRate != 0 || s.LoopRate != 0 {
			t.Fatalf("evict=%v: old session should be out of window, got err=%v loop=%v", evict, s.ToolErrRate, s.LoopRate)
		}
	}
}

// A loop seen before an eviction cycle still makes its task bad afterwards.
func TestLoopStatusSurvivesEvict(t *testing.T) {
	e := NewEngine(Config{Loop: LoopConfig{Threshold: 3, IdleTTL: time.Hour}, SLO: BurnConfig{Objective: 0.9}})
	for i := 0; i < 3; i++ {
		e.Observe(tool("loop", "search", "same", false, t0.Add(time.Duration(i)*time.Second)))
	}
	e.Evict(t0.Add(time.Minute))
	e.Observe(task("loop", false, t0, 2*time.Minute))
	if b := e.Snapshot(t0.Add(3 * time.Minute))[0].BurnRate1h; b < 9.99 {
		t.Fatalf("looping task should be bad (burn 10), got %v", b)
	}
}
