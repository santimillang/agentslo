package agentsloconnector

import (
	"context"
	"sort"
	"sync"
	"time"

	"go.opentelemetry.io/collector/component"
	"go.opentelemetry.io/collector/consumer"
	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/pmetric"
	"go.opentelemetry.io/collector/pdata/ptrace"
	"go.uber.org/zap"

	"github.com/santimillang/agentslo/internal/detect"
)

type agentsloConnector struct {
	logger *zap.Logger
	cfg    *Config
	next   consumer.Metrics
	engine *detect.Engine

	mu         sync.Mutex
	loopCounts map[loopKey]int64 // cumulative loop events

	start  pcommon.Timestamp
	cancel context.CancelFunc
	done   chan struct{}
}

type loopKey struct {
	agent string
	kind  detect.LoopKind
}

func newConnector(logger *zap.Logger, cfg *Config, next consumer.Metrics) *agentsloConnector {
	return &agentsloConnector{
		logger:     logger,
		cfg:        cfg,
		next:       next,
		engine:     detect.NewEngine(cfg.engineConfig()),
		loopCounts: map[loopKey]int64{},
	}
}

func (c *agentsloConnector) Capabilities() consumer.Capabilities {
	return consumer.Capabilities{MutatesData: false}
}

func (c *agentsloConnector) ConsumeTraces(_ context.Context, td ptrace.Traces) error {
	spans := toSpans(td)
	// Feed in end order so task spans are scored after their tool calls.
	sort.Slice(spans, func(i, j int) bool { return spans[i].End.Before(spans[j].End) })
	for _, s := range spans {
		for _, ev := range c.engine.Observe(s) {
			c.logger.Info("agent loop detected",
				zap.String("agent", ev.Agent),
				zap.String("kind", string(ev.Kind)),
				zap.String("tool", ev.Tool),
				zap.String("session", ev.SessionID),
				zap.Int("count", ev.Count))
			c.mu.Lock()
			c.loopCounts[loopKey{ev.Agent, ev.Kind}]++
			c.mu.Unlock()
		}
	}
	return nil
}

func (c *agentsloConnector) Start(_ context.Context, _ component.Host) error {
	c.start = pcommon.NewTimestampFromTime(time.Now())
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.done = make(chan struct{})
	go c.loop(ctx)
	return nil
}

func (c *agentsloConnector) Shutdown(context.Context) error {
	if c.cancel != nil {
		c.cancel()
		<-c.done
	}
	return nil
}

func (c *agentsloConnector) loop(ctx context.Context) {
	defer close(c.done)
	emit := time.NewTicker(c.cfg.EmitInterval)
	defer emit.Stop()
	ttl := c.cfg.Loop.IdleTTL
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	evict := time.NewTicker(ttl)
	defer evict.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-evict.C:
			c.engine.Evict(now)
		case now := <-emit.C:
			if err := c.next.ConsumeMetrics(ctx, c.buildMetrics(now)); err != nil {
				c.logger.Warn("failed to emit agentslo metrics", zap.Error(err))
			}
		}
	}
}

func (c *agentsloConnector) buildMetrics(now time.Time) pmetric.Metrics {
	md := pmetric.NewMetrics()
	sm := md.ResourceMetrics().AppendEmpty().ScopeMetrics().AppendEmpty()
	sm.Scope().SetName("github.com/santimillang/agentslo")
	ts := pcommon.NewTimestampFromTime(now)

	gauge := func(name, unit, desc string) pmetric.NumberDataPointSlice {
		m := sm.Metrics().AppendEmpty()
		m.SetName(name)
		m.SetUnit(unit)
		m.SetDescription(desc)
		return m.SetEmptyGauge().DataPoints()
	}
	point := func(dps pmetric.NumberDataPointSlice, v float64, attrs ...string) {
		dp := dps.AppendEmpty()
		dp.SetTimestamp(ts)
		dp.SetDoubleValue(v)
		for i := 0; i+1 < len(attrs); i += 2 {
			dp.Attributes().PutStr(attrs[i], attrs[i+1])
		}
	}

	burn := gauge("agentslo.burn_rate", "1", "Error-budget burn rate on agent tasks (1 = on budget)")
	health := gauge("agentslo.health", "1", "Agent health score, 0 (on fire) to 100")
	toolErr := gauge("agentslo.tool.error_ratio", "1", "Failed tool calls / tool calls")
	loopRate := gauge("agentslo.loop.session_ratio", "1", "Sessions with a loop event / sessions")
	firing := gauge("agentslo.alert.firing", "1", "1 while a multi-window burn-rate alert is firing")

	for _, s := range c.engine.Snapshot(now) {
		point(burn, s.BurnRate1h, "gen_ai.agent.name", s.Agent, "window", "1h")
		point(burn, s.BurnRate6h, "gen_ai.agent.name", s.Agent, "window", "6h")
		point(health, s.Health, "gen_ai.agent.name", s.Agent)
		point(toolErr, s.ToolErrRate, "gen_ai.agent.name", s.Agent)
		point(loopRate, s.LoopRate, "gen_ai.agent.name", s.Agent)
		for _, rule := range s.Firing {
			point(firing, 1, "gen_ai.agent.name", s.Agent, "rule", rule)
		}
	}

	loops := sm.Metrics().AppendEmpty()
	loops.SetName("agentslo.loop.events")
	loops.SetDescription("Loop patterns detected, by kind")
	sum := loops.SetEmptySum()
	sum.SetIsMonotonic(true)
	sum.SetAggregationTemporality(pmetric.AggregationTemporalityCumulative)
	c.mu.Lock()
	for k, n := range c.loopCounts {
		dp := sum.DataPoints().AppendEmpty()
		dp.SetStartTimestamp(c.start)
		dp.SetTimestamp(ts)
		dp.SetIntValue(n)
		dp.Attributes().PutStr("gen_ai.agent.name", k.agent)
		dp.Attributes().PutStr("kind", string(k.kind))
	}
	c.mu.Unlock()
	return md
}
