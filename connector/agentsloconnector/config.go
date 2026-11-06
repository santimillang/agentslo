package agentsloconnector

import (
	"errors"
	"time"

	"github.com/santimillang/agentslo/internal/detect"
)

// Config for the agentslo connector (traces -> metrics).
type Config struct {
	// Objective is the SLO target on agent tasks, e.g. 0.99.
	Objective float64 `mapstructure:"objective"`
	// LatencyTarget makes a task "bad" if it runs longer. 0 disables it.
	LatencyTarget time.Duration `mapstructure:"latency_target"`
	// EmitInterval is how often metrics are pushed downstream.
	EmitInterval time.Duration `mapstructure:"emit_interval"`
	// RatioWindow is the rolling window for the tool-error and loop-session
	// ratios (and the health inputs built from them).
	RatioWindow time.Duration `mapstructure:"ratio_window"`

	Loop LoopConfig `mapstructure:"loop"`
}

type LoopConfig struct {
	Window    int           `mapstructure:"window"`
	Threshold int           `mapstructure:"threshold"`
	IdleTTL   time.Duration `mapstructure:"idle_ttl"`
}

func createDefaultConfig() *Config {
	return &Config{
		Objective:    0.99,
		EmitInterval: 15 * time.Second,
		RatioWindow:  time.Hour,
		Loop:         LoopConfig{Window: 10, Threshold: 4, IdleTTL: 30 * time.Minute},
	}
}

func (c *Config) Validate() error {
	if c.Objective <= 0 || c.Objective >= 1 {
		return errors.New("objective must be in (0, 1)")
	}
	if c.EmitInterval <= 0 {
		return errors.New("emit_interval must be positive")
	}
	if c.RatioWindow <= 0 {
		return errors.New("ratio_window must be positive")
	}
	return nil
}

func (c *Config) engineConfig() detect.Config {
	return detect.Config{
		Loop:        detect.LoopConfig{Window: c.Loop.Window, Threshold: c.Loop.Threshold, IdleTTL: c.Loop.IdleTTL},
		SLO:         detect.BurnConfig{Objective: c.Objective, LatencyTarget: c.LatencyTarget},
		RatioWindow: c.RatioWindow,
	}
}
