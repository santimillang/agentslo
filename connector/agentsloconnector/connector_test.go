package agentsloconnector

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/collector/component/componenttest"
	"go.opentelemetry.io/collector/connector/connectortest"
	"go.opentelemetry.io/collector/consumer/consumertest"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/santimillang/agentslo/internal/detect"
)

// --- trace building helpers ---------------------------------------------

type spanSpec struct {
	op, agent, session, tool, args string
	traceByte                      byte
	start, end                     time.Time
	err                            bool
	errType                        string
	inTok, outTok                  int64
}

// newTraces returns one resource (with service.name) and a function that
// appends spans to it.
func newTraces(service string) (ptrace.Traces, func(spanSpec)) {
	td := ptrace.NewTraces()
	rs := td.ResourceSpans().AppendEmpty()
	if service != "" {
		rs.Resource().Attributes().PutStr(attrServiceName, service)
	}
	spans := rs.ScopeSpans().AppendEmpty().Spans()
	add := func(s spanSpec) {
		sp := spans.AppendEmpty()
		sp.SetName(s.op)
		sp.SetTraceID(pcommon.TraceID{s.traceByte})
		sp.SetStartTimestamp(pcommon.NewTimestampFromTime(s.start))
		sp.SetEndTimestamp(pcommon.NewTimestampFromTime(s.end))
		a := sp.Attributes()
		if s.op != "" {
			a.PutStr(attrOperation, s.op)
		}
		if s.agent != "" {
			a.PutStr(attrAgentName, s.agent)
		}
		if s.session != "" {
			a.PutStr(attrConversation, s.session)
		}
		if s.tool != "" {
			a.PutStr(attrToolName, s.tool)
		}
		if s.args != "" {
			a.PutStr(attrToolArgs, s.args)
		}
		if s.errType != "" {
			a.PutStr(attrErrorType, s.errType)
		}
		if s.inTok != 0 {
			a.PutInt(attrInputTokens, s.inTok)
		}
		if s.outTok != 0 {
			a.PutInt(attrOutputTokens, s.outTok)
		}
		if s.err {
			sp.Status().SetCode(ptrace.StatusCodeError)
		}
	}
	return td, add
}

// --- metric lookup helpers ------------------------------------------------

func findMetric(t *testing.T, md pmetric.Metrics, name string) pmetric.Metric {
	t.Helper()
	rms := md.ResourceMetrics()
	for i := 0; i < rms.Len(); i++ {
		sms := rms.At(i).ScopeMetrics()
		for j := 0; j < sms.Len(); j++ {
			ms := sms.At(j).Metrics()
			for k := 0; k < ms.Len(); k++ {
				if ms.At(k).Name() == name {
					return ms.At(k)
				}
			}
		}
	}
	t.Fatalf("metric %q not found", name)
	return pmetric.Metric{}
}

func hasAttrs(m pcommon.Map, kv ...string) bool {
	for i := 0; i+1 < len(kv); i += 2 {
		v, ok := m.Get(kv[i])
		if !ok || v.AsString() != kv[i+1] {
			return false
		}
	}
	return true
}

// gaugeValue returns the gauge datapoint whose attributes include kv.
func gaugeValue(t *testing.T, md pmetric.Metrics, name string, kv ...string) float64 {
	t.Helper()
	dps := findMetric(t, md, name).Gauge().DataPoints()
	for i := 0; i < dps.Len(); i++ {
		if hasAttrs(dps.At(i).Attributes(), kv...) {
			return dps.At(i).DoubleValue()
		}
	}
	t.Fatalf("%s: no datapoint with attrs %v", name, kv)
	return 0
}

// --- tests ------------------------------------------------------------------

func TestFactory(t *testing.T) {
	f := NewFactory()
	if got := f.Type().String(); got != "agentslo" {
		t.Fatalf("type = %q, want agentslo", got)
	}
	cfg := f.CreateDefaultConfig()
	if err := cfg.(*Config).Validate(); err != nil {
		t.Fatalf("default config invalid: %v", err)
	}
	c, err := f.CreateTracesToMetrics(context.Background(),
		connectortest.NewNopSettings(f.Type()), cfg, new(consumertest.MetricsSink))
	if err != nil || c == nil {
		t.Fatalf("CreateTracesToMetrics: %v, %v", c, err)
	}
	if c.Capabilities().MutatesData {
		t.Fatal("connector must not mutate data")
	}
}

func TestConfigValidate(t *testing.T) {
	for name, mut := range map[string]func(*Config){
		"objective zero": func(c *Config) { c.Objective = 0 },
		"objective one":  func(c *Config) { c.Objective = 1 },
		"emit zero":      func(c *Config) { c.EmitInterval = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := createDefaultConfig()
			mut(c)
			if c.Validate() == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestToSpans(t *testing.T) {
	now := time.Now()
	td, add := newTraces("svc-fallback")
	add(spanSpec{op: "HTTP GET", start: now, end: now}) // not GenAI: skipped
	add(spanSpec{op: "chat", agent: "bot", session: "s1", start: now, end: now, inTok: 10, outTok: 5})
	add(spanSpec{op: "execute_tool", session: "s1", tool: "search", args: `{"q":"secret user text"}`, start: now, end: now, err: true})
	add(spanSpec{op: "invoke_agent", agent: "bot", session: "s1", start: now, end: now, errType: "timeout"})

	spans := toSpans(td)
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want 3 (non-GenAI skipped)", len(spans))
	}

	llm, tool, task := spans[0], spans[1], spans[2]
	if llm.Kind != detect.KindLLM || llm.InputTokens != 10 || llm.OutputTokens != 5 {
		t.Errorf("llm span = %+v", llm)
	}
	if tool.Agent != "svc-fallback" {
		t.Errorf("agent fallback = %q, want service.name", tool.Agent)
	}
	if !tool.Error {
		t.Error("status=ERROR should mark tool span as error")
	}
	if len(tool.ArgsHash) != 16 || strings.Contains(tool.ArgsHash, "secret") {
		t.Errorf("args must be hashed, got %q", tool.ArgsHash)
	}
	if task.Kind != detect.KindAgent || !task.Error || task.ErrorType != "timeout" {
		t.Errorf("error.type should mark task as error: %+v", task)
	}
}

func TestArgsHashStable(t *testing.T) {
	if argsHash("") != "" {
		t.Error("empty args should hash to empty")
	}
	if argsHash("a") != argsHash("a") || argsHash("a") == argsHash("b") {
		t.Error("hash must be deterministic and distinguish inputs")
	}
}

// TestConsumeTracesMetrics feeds a healthy agent and a looping agent and
// checks the metrics that come out.
func TestConsumeTracesMetrics(t *testing.T) {
	c := newConnector(zap.NewNop(), createDefaultConfig(), new(consumertest.MetricsSink))
	now := time.Now()
	at := func(sec int) time.Time { return now.Add(time.Duration(sec-60) * time.Second) }

	td, add := newTraces("")
	// support-bot: two clean tasks, each with one successful tool call.
	for i, sess := range []string{"ok-1", "ok-2"} {
		b := byte(1 + i)
		add(spanSpec{op: "execute_tool", agent: "support-bot", session: sess, tool: "kb", args: sess, traceByte: b, start: at(0), end: at(1)})
		add(spanSpec{op: "invoke_agent", agent: "support-bot", session: sess, traceByte: b, start: at(0), end: at(2)})
	}
	// research-agent: same tool + args 4 times (threshold) then the task ends.
	// Spans are appended out of end order to check ConsumeTraces sorts them.
	add(spanSpec{op: "invoke_agent", agent: "research-agent", session: "loop-1", traceByte: 9, start: at(0), end: at(10)})
	for i := 0; i < 4; i++ {
		add(spanSpec{op: "execute_tool", agent: "research-agent", session: "loop-1", tool: "web_search",
			args: `{"q":"same"}`, traceByte: 9, start: at(i), end: at(i + 1), err: i == 0})
	}

	if err := c.ConsumeTraces(context.Background(), td); err != nil {
		t.Fatal(err)
	}
	md := c.buildMetrics(now)

	for _, name := range []string{"agentslo.burn_rate", "agentslo.health", "agentslo.tool.error_ratio", "agentslo.loop.session_ratio", "agentslo.loop.events"} {
		findMetric(t, md, name)
	}

	// Burn: support-bot all good; research-agent 1/1 bad => 1.0/0.01 = 100.
	if v := gaugeValue(t, md, "agentslo.burn_rate", attrAgentName, "support-bot", "window", "1h"); v != 0 {
		t.Errorf("support-bot burn 1h = %v, want 0", v)
	}
	if v := gaugeValue(t, md, "agentslo.burn_rate", attrAgentName, "research-agent", "window", "1h"); v < 99.9 || v > 100.1 {
		t.Errorf("research-agent burn 1h = %v, want 100", v)
	}

	healthy := gaugeValue(t, md, "agentslo.health", attrAgentName, "support-bot")
	looping := gaugeValue(t, md, "agentslo.health", attrAgentName, "research-agent")
	if !(looping < healthy) {
		t.Errorf("health: research-agent %v should be below support-bot %v", looping, healthy)
	}
	if healthy != 100 {
		t.Errorf("support-bot health = %v, want 100", healthy)
	}

	if v := gaugeValue(t, md, "agentslo.tool.error_ratio", attrAgentName, "research-agent"); v != 0.25 {
		t.Errorf("tool error ratio = %v, want 0.25", v)
	}
	if v := gaugeValue(t, md, "agentslo.loop.session_ratio", attrAgentName, "research-agent"); v != 1 {
		t.Errorf("loop session ratio = %v, want 1", v)
	}

	// Burn rate 100 trips the fast-burn alert.
	firing := findMetric(t, md, "agentslo.alert.firing").Gauge().DataPoints()
	if firing.Len() == 0 {
		t.Error("expected an alert firing for research-agent")
	}
	for i := 0; i < firing.Len(); i++ {
		if !hasAttrs(firing.At(i).Attributes(), attrAgentName, "research-agent") {
			t.Errorf("unexpected firing datapoint: %v", firing.At(i).Attributes().AsRaw())
		}
	}

	loops := findMetric(t, md, "agentslo.loop.events").Sum()
	if !loops.IsMonotonic() || loops.AggregationTemporality() != pmetric.AggregationTemporalityCumulative {
		t.Error("loop.events must be a cumulative monotonic sum")
	}
	if loops.DataPoints().Len() != 1 {
		t.Fatalf("loop events datapoints = %d, want 1", loops.DataPoints().Len())
	}
	dp := loops.DataPoints().At(0)
	if dp.IntValue() != 1 || !hasAttrs(dp.Attributes(), attrAgentName, "research-agent", "kind", string(detect.LoopRepeat)) {
		t.Errorf("loop event dp = %d %v", dp.IntValue(), dp.Attributes().AsRaw())
	}
}

// TestLifecycleEmits checks Start runs the emit ticker into the next consumer
// and Shutdown stops it.
func TestLifecycleEmits(t *testing.T) {
	cfg := createDefaultConfig()
	cfg.EmitInterval = 10 * time.Millisecond
	sink := new(consumertest.MetricsSink)
	c := newConnector(zap.NewNop(), cfg, sink)

	if err := c.Start(context.Background(), componenttest.NewNopHost()); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for len(sink.AllMetrics()) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(sink.AllMetrics()) == 0 {
		t.Fatal("no metrics emitted after Start")
	}
	n := len(sink.AllMetrics())
	time.Sleep(50 * time.Millisecond)
	if len(sink.AllMetrics()) != n {
		t.Error("metrics still emitted after Shutdown")
	}
}

func TestShutdownWithoutStart(t *testing.T) {
	c := newConnector(zap.NewNop(), createDefaultConfig(), new(consumertest.MetricsSink))
	if err := c.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}
