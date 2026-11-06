package detect

import "math"

// HealthInputs are the per-agent signals folded into one score.
type HealthInputs struct {
	BurnRate1h  float64 // from BurnTracker; 0 when no traffic
	ToolErrRate float64 // failed tool calls / tool calls, 0..1
	LoopRate    float64 // sessions with a loop event / sessions, 0..1
	CostRatio   float64 // current cost per task / baseline cost per task; 0 = unknown
}

// HealthWeights must sum to 1. Defaults favour budget burn, like the Kafka
// cluster health scores this is modelled on: the SLO is the thing users feel,
// the rest are leading indicators.
type HealthWeights struct {
	Burn, ToolErr, Loop, Cost float64
}

var DefaultHealthWeights = HealthWeights{Burn: 0.4, ToolErr: 0.2, Loop: 0.25, Cost: 0.15}

// Health returns 0 (on fire) .. 100 (healthy). Each component is mapped to a
// 0..1 penalty with a saturating curve so one terrible signal cannot be hidden
// by three good ones, but also cannot exceed its weight.
func Health(in HealthInputs, w HealthWeights) float64 {
	pen := w.Burn*sat(in.BurnRate1h, 6) + // 6x budget burn = full penalty
		w.ToolErr*sat(in.ToolErrRate, 0.25) + // 25% tool errors = full penalty
		w.Loop*sat(in.LoopRate, 0.10) // 10% of sessions looping = full penalty
	if in.CostRatio > 1 {
		pen += w.Cost * sat(in.CostRatio-1, 2) // 3x baseline cost = full penalty
	}
	return math.Round((1-pen)*1000) / 10
}

// sat maps x in [0, limit] linearly to [0, 1] and clamps above.
func sat(x, limit float64) float64 {
	if x <= 0 {
		return 0
	}
	return math.Min(x/limit, 1)
}
