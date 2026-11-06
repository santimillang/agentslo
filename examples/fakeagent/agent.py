"""Fake agent that emits OTel GenAI spans with known failure patterns.

No model is called: every LLM and tool step is simulated, so the collector
sees realistic span shapes and we know exactly which sessions should trip
which detector.

SCENARIO env var: healthy | loop | retry_storm | oscillation | slow | mixed
"""

import json
import os
import random
import time
import uuid

from opentelemetry import trace
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.trace import Status, StatusCode

AGENTS = ["support-bot", "research-agent"]
TOOLS = ["search_docs", "lookup_order", "db_query", "send_email"]

provider = TracerProvider(resource=Resource.create({"service.name": "fakeagent"}))
provider.add_span_processor(BatchSpanProcessor(OTLPSpanExporter()))
trace.set_tracer_provider(provider)
tracer = trace.get_tracer("agentslo.fakeagent")


def llm_step(agent: str, model: str = "claude-sonnet-5-5"):
    with tracer.start_as_current_span(f"chat {model}") as s:
        s.set_attribute("gen_ai.operation.name", "chat")
        s.set_attribute("gen_ai.agent.name", agent)
        s.set_attribute("gen_ai.request.model", model)
        s.set_attribute("gen_ai.usage.input_tokens", random.randint(800, 4000))
        s.set_attribute("gen_ai.usage.output_tokens", random.randint(50, 600))
        time.sleep(random.uniform(0.02, 0.08))


def tool_step(agent: str, conv: str, tool: str, args: dict, fail: bool = False):
    with tracer.start_as_current_span(f"execute_tool {tool}") as s:
        s.set_attribute("gen_ai.operation.name", "execute_tool")
        s.set_attribute("gen_ai.agent.name", agent)
        s.set_attribute("gen_ai.conversation.id", conv)
        s.set_attribute("gen_ai.tool.name", tool)
        s.set_attribute("gen_ai.tool.call.arguments", json.dumps(args, sort_keys=True))
        time.sleep(random.uniform(0.005, 0.03))
        if fail:
            s.set_attribute("error.type", "ToolTimeout")
            s.set_status(Status(StatusCode.ERROR, "tool timed out"))


def run_task(agent: str, kind: str):
    conv = str(uuid.uuid4())
    with tracer.start_as_current_span(f"invoke_agent {agent}") as task:
        task.set_attribute("gen_ai.operation.name", "invoke_agent")
        task.set_attribute("gen_ai.agent.name", agent)
        task.set_attribute("gen_ai.conversation.id", conv)

        if kind == "healthy":
            for _ in range(random.randint(1, 3)):
                llm_step(agent)
                tool_step(agent, conv, random.choice(TOOLS), {"q": uuid.uuid4().hex[:6]})
            llm_step(agent)

        elif kind == "loop":
            # Same call, same args, over and over. Task "succeeds" at the end,
            # which is exactly why plain error-rate dashboards miss it.
            for _ in range(8):
                llm_step(agent)
                tool_step(agent, conv, "search_docs", {"q": "refund policy"})

        elif kind == "retry_storm":
            for i in range(6):
                llm_step(agent)
                tool_step(agent, conv, "db_query", {"attempt": i}, fail=True)
            task.set_status(Status(StatusCode.ERROR, "gave up"))
            task.set_attribute("error.type", "ToolUnavailable")

        elif kind == "oscillation":
            for i in range(10):
                llm_step(agent)
                tool = "lookup_order" if i % 2 == 0 else "search_docs"
                tool_step(agent, conv, tool, {"id": "A-1"})

        elif kind == "slow":
            llm_step(agent)
            time.sleep(1.5)  # pair with a low latency_target to see it count as bad
            llm_step(agent)


def pick(scenario: str) -> str:
    if scenario != "mixed":
        return scenario
    return random.choices(
        ["healthy", "loop", "retry_storm", "oscillation", "slow"],
        weights=[85, 5, 4, 3, 3],
    )[0]


def main():
    scenario = os.environ.get("SCENARIO", "mixed")
    rate = float(os.environ.get("TASKS_PER_SEC", "4"))
    print(f"fakeagent: scenario={scenario} rate={rate}/s", flush=True)
    while True:
        agent = random.choice(AGENTS)
        # research-agent is the flaky one, so the two agents' scores diverge.
        kind = pick(scenario) if agent == "research-agent" else pick("healthy" if scenario == "mixed" else scenario)
        run_task(agent, kind)
        time.sleep(1 / rate)


if __name__ == "__main__":
    main()
