package agentsloconnector

import (
	"crypto/sha256"
	"encoding/hex"

	"go.opentelemetry.io/collector/pdata/pcommon"
	"go.opentelemetry.io/collector/pdata/ptrace"

	"github.com/santimillang/agentslo/internal/detect"
)

// OTel GenAI semantic convention attribute keys.
const (
	attrOperation    = "gen_ai.operation.name"
	attrAgentName    = "gen_ai.agent.name"
	attrConversation = "gen_ai.conversation.id"
	attrModel        = "gen_ai.request.model"
	attrToolName     = "gen_ai.tool.name"
	attrToolArgs     = "gen_ai.tool.call.arguments"
	attrInputTokens  = "gen_ai.usage.input_tokens"
	attrOutputTokens = "gen_ai.usage.output_tokens"
	attrErrorType    = "error.type"
	attrServiceName  = "service.name"
)

func kindOf(op string) detect.Kind {
	switch op {
	case "chat", "text_completion", "generate_content":
		return detect.KindLLM
	case "execute_tool":
		return detect.KindTool
	case "invoke_agent", "create_agent":
		return detect.KindAgent
	}
	return detect.KindOther
}

func str(m pcommon.Map, k string) string {
	if v, ok := m.Get(k); ok {
		return v.AsString()
	}
	return ""
}

func num(m pcommon.Map, k string) int64 {
	if v, ok := m.Get(k); ok && v.Type() == pcommon.ValueTypeInt {
		return v.Int()
	}
	return 0
}

// argsHash fingerprints tool arguments without keeping them: the detector
// only needs equality, and arguments may hold user data.
func argsHash(args string) string {
	if args == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(args))
	return hex.EncodeToString(sum[:8])
}

// toSpans flattens the GenAI spans in a batch. Non-GenAI spans are skipped.
func toSpans(td ptrace.Traces) []detect.Span {
	var out []detect.Span
	rss := td.ResourceSpans()
	for i := 0; i < rss.Len(); i++ {
		rs := rss.At(i)
		service := str(rs.Resource().Attributes(), attrServiceName)
		sss := rs.ScopeSpans()
		for j := 0; j < sss.Len(); j++ {
			spans := sss.At(j).Spans()
			for k := 0; k < spans.Len(); k++ {
				sp := spans.At(k)
				a := sp.Attributes()
				kind := kindOf(str(a, attrOperation))
				if kind == detect.KindOther {
					continue
				}
				agent := str(a, attrAgentName)
				if agent == "" {
					agent = service
				}
				errType := str(a, attrErrorType)
				out = append(out, detect.Span{
					TraceID:      sp.TraceID().String(),
					SessionID:    str(a, attrConversation),
					Agent:        agent,
					Kind:         kind,
					Model:        str(a, attrModel),
					Tool:         str(a, attrToolName),
					ArgsHash:     argsHash(str(a, attrToolArgs)),
					Start:        sp.StartTimestamp().AsTime(),
					End:          sp.EndTimestamp().AsTime(),
					Error:        sp.Status().Code() == ptrace.StatusCodeError || errType != "",
					ErrorType:    errType,
					InputTokens:  num(a, attrInputTokens),
					OutputTokens: num(a, attrOutputTokens),
				})
			}
		}
	}
	return out
}
