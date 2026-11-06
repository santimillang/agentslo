package detect

import (
	"sync"
	"time"
)

// BurnConfig defines one SLO on agent tasks (KindAgent spans).
// "Good" means: no error, finished within LatencyTarget (if set), and no loop
// was detected in its session.
type BurnConfig struct {
	Objective     float64       // e.g. 0.99
	LatencyTarget time.Duration // 0 disables the latency condition
	Bucket        time.Duration // resolution of the ring buffer (default 1m)
	Retention     time.Duration // longest window tracked (default 6h)
}

func (c BurnConfig) withDefaults() BurnConfig {
	if c.Objective <= 0 || c.Objective >= 1 {
		c.Objective = 0.99
	}
	if c.Bucket <= 0 {
		c.Bucket = time.Minute
	}
	if c.Retention <= 0 {
		c.Retention = 6 * time.Hour
	}
	return c
}

// AlertRule is one multi-window, multi-burn-rate rule from the Google SRE
// workbook: fire when BOTH windows burn faster than Factor.
type AlertRule struct {
	Name   string
	Long   time.Duration
	Short  time.Duration
	Factor float64
}

// DefaultRules: page on 2% of a 30d budget in 1h, ticket on 5% in 6h.
var DefaultRules = []AlertRule{
	{Name: "page", Long: time.Hour, Short: 5 * time.Minute, Factor: 14.4},
	{Name: "ticket", Long: 6 * time.Hour, Short: 30 * time.Minute, Factor: 6},
}

type bucket struct {
	start       int64 // unix bucket index
	good, total int64
}

type ring struct {
	buckets []bucket
}

func (r *ring) add(idx int64, good bool) {
	b := &r.buckets[idx%int64(len(r.buckets))]
	if b.start != idx {
		*b = bucket{start: idx}
	}
	b.total++
	if good {
		b.good++
	}
}

func (r *ring) sum(fromIdx, toIdx int64) (good, total int64) {
	for _, b := range r.buckets {
		if b.start >= fromIdx && b.start <= toIdx {
			good += b.good
			total += b.total
		}
	}
	return
}

// BurnTracker keeps good/total task counts per agent and computes burn rates.
type BurnTracker struct {
	cfg   BurnConfig
	mu    sync.Mutex
	rings map[string]*ring
}

func NewBurnTracker(cfg BurnConfig) *BurnTracker {
	return &BurnTracker{cfg: cfg.withDefaults(), rings: map[string]*ring{}}
}

func (t *BurnTracker) idx(ts time.Time) int64 { return ts.UnixNano() / int64(t.cfg.Bucket) }

// Good reports whether a task span meets the SLO.
func (t *BurnTracker) Good(s Span, looped bool) bool {
	if s.Error || looped {
		return false
	}
	return t.cfg.LatencyTarget == 0 || s.Duration() <= t.cfg.LatencyTarget
}

// Record counts one finished task for its agent.
func (t *BurnTracker) Record(agent string, at time.Time, good bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, ok := t.rings[agent]
	if !ok {
		n := int(t.cfg.Retention/t.cfg.Bucket) + 1
		r = &ring{buckets: make([]bucket, n)}
		for i := range r.buckets {
			r.buckets[i].start = -1
		}
		t.rings[agent] = r
	}
	r.add(t.idx(at), good)
}

// BurnRate is the error rate over the window divided by the error budget
// (1 - objective). 1.0 means burning exactly at budget. ok=false means no
// traffic in the window.
func (t *BurnTracker) BurnRate(agent string, now time.Time, window time.Duration) (rate float64, ok bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	r, found := t.rings[agent]
	if !found {
		return 0, false
	}
	to := t.idx(now)
	from := to - int64(window/t.cfg.Bucket) + 1
	good, total := r.sum(from, to)
	if total == 0 {
		return 0, false
	}
	errRate := float64(total-good) / float64(total)
	return errRate / (1 - t.cfg.Objective), true
}

// Firing returns the rules currently firing for an agent.
func (t *BurnTracker) Firing(agent string, now time.Time, rules []AlertRule) []AlertRule {
	var out []AlertRule
	for _, r := range rules {
		long, ok1 := t.BurnRate(agent, now, r.Long)
		short, ok2 := t.BurnRate(agent, now, r.Short)
		if ok1 && ok2 && long >= r.Factor && short >= r.Factor {
			out = append(out, r)
		}
	}
	return out
}

// Agents lists the agents with recorded traffic.
func (t *BurnTracker) Agents() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.rings))
	for a := range t.rings {
		out = append(out, a)
	}
	return out
}
