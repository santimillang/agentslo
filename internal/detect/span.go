// Package detect holds the backend-agnostic core of agentslo: it consumes a
// minimal view of GenAI spans and derives reliability signals (tool-call
// loops, SLO burn rates, a per-agent health score). It depends only on the
// standard library so it can be tested without a collector.
package detect

import "time"

// Kind is the coarse role a span plays in an agent run, derived from
// gen_ai.operation.name.
type Kind int

const (
	KindOther Kind = iota
	KindLLM        // chat, text_completion, generate_content
	KindTool       // execute_tool
	KindAgent      // invoke_agent / create_agent: one task, the unit SLOs are measured on
)

// Span is the subset of an OTel GenAI span agentslo cares about.
type Span struct {
	TraceID   string
	SessionID string // gen_ai.conversation.id, falls back to TraceID
	Agent     string // gen_ai.agent.name, falls back to service.name
	Kind      Kind
	Model     string // gen_ai.request.model
	Tool      string // gen_ai.tool.name
	ArgsHash  string // stable hash of gen_ai.tool.call.arguments ("" if not captured)
	Start     time.Time
	End       time.Time
	Error     bool   // status=ERROR or error.type set
	ErrorType string // error.type

	InputTokens  int64
	OutputTokens int64
}

// Duration of the span.
func (s Span) Duration() time.Duration { return s.End.Sub(s.Start) }

// Session returns the key loops are tracked under.
func (s Span) Session() string {
	if s.SessionID != "" {
		return s.SessionID
	}
	return s.TraceID
}
