// Package agent provides semantic conventions for AI agent observability.
//
// This package implements the Agent Trace Standard for tracing multi-step agent
// workflows with LLM calls, tool use, human-in-the-loop, and agent-to-agent handoffs.
//
// # Agent Sessions
//
// An agent session represents a complete agent execution from goal to completion.
// It groups all spans (LLM calls, tool calls, human interactions) that belong to
// one logical task.
//
//	session := agent.StartSession(ctx, client, agent.SessionConfig{
//	    Name:      "travel-assistant",
//	    Goal:      "Book flight to Amsterdam",
//	    Framework: "custom",
//	})
//	defer session.End()
//
// # LLM Steps
//
// LLM steps represent calls to language models:
//
//	session.LLMStep(ctx, "planning", func(step *agent.Step) {
//	    step.SetReasoning("Understanding user requirements")
//	    response := callClaude(planningPrompt)
//	    step.SetLLMResponse(agent.LLMResponse{
//	        TokensIn:  response.Usage.InputTokens,
//	        TokensOut: response.Usage.OutputTokens,
//	        CostUSD:   0.02,
//	    })
//	})
//
// # Tool Steps
//
// Tool steps represent function/tool executions:
//
//	session.ToolStep(ctx, "search_flights", searchInput, func(step *agent.Step) {
//	    results, err := searchFlights(searchInput)
//	    if err != nil {
//	        step.SetError(err)
//	        return
//	    }
//	    step.SetOutput(results)
//	})
//
// # Human-in-the-Loop Steps
//
// Human steps represent waiting for or receiving human input:
//
//	session.HumanStep(ctx, "approval", func(step *agent.Step) {
//	    approved := waitForUserApproval()
//	    if approved {
//	        step.SetHumanAction(agent.HumanActionApproved)
//	    } else {
//	        step.SetHumanAction(agent.HumanActionRejected)
//	    }
//	})
//
// # Agent Handoffs
//
// Handoffs delegate work to another agent:
//
//	newSessionID := session.Handoff(ctx, "booking-agent", "Specialized for bookings")
//
// # Rollup Statistics
//
// The session automatically tracks rollup statistics:
//   - Total LLM cost in USD
//   - Total input and output tokens
//   - Number of LLM calls, tool calls, and human interactions
//
// These are recorded on the session span when End() is called.
//
// # Semantic Attributes
//
// All attribute keys follow the Agent Trace Standard and are exported as constants
// (e.g., AttrSessionID, AttrStepType, AttrToolName, etc.).
package agent
