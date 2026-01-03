package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/tedo-ai/imprint-go"
)

// LLMStep creates an LLM step span and executes the given function.
// The step is automatically ended when the function returns.
//
// Usage:
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
func (s *Session) LLMStep(ctx context.Context, name string, fn func(*Step)) {
	stepIndex := s.nextStepIndex()

	// Create span name following convention: agent.{agent_name}.llm.{purpose}
	spanName := fmt.Sprintf("agent.%s.llm.%s", s.config.Name, name)

	ctx, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "client"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeLLM))
	span.SetAttribute(AttrStepName, name)

	step := &Step{
		span:      span,
		session:   s,
		stepType:  StepTypeLLM,
		startTime: time.Now(),
	}

	// Execute the function
	fn(step)

	// End the step
	step.end()
}

// ToolStep creates a tool step span and executes the given function.
// The input is automatically recorded on the span.
//
// Usage:
//
//	session.ToolStep(ctx, "search_flights", searchInput, func(step *agent.Step) {
//	    results, err := searchFlights(searchInput)
//	    if err != nil {
//	        step.SetError(err)
//	        return
//	    }
//	    step.SetOutput(results)
//	})
func (s *Session) ToolStep(ctx context.Context, name string, input interface{}, fn func(*Step)) {
	stepIndex := s.nextStepIndex()
	s.incrementToolCalls()

	// Create span name following convention: agent.{agent_name}.tool.{tool_name}
	spanName := fmt.Sprintf("agent.%s.tool.%s", s.config.Name, name)

	ctx, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "client"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeTool))
	span.SetAttribute(AttrStepName, name)

	// Set tool attributes
	span.SetAttribute(AttrToolName, name)

	// Encode and set input
	var inputStr string
	switch v := input.(type) {
	case string:
		inputStr = v
	case []byte:
		inputStr = string(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			inputStr = "<error encoding input>"
		} else {
			inputStr = string(data)
		}
	}
	span.SetAttribute(AttrToolInput, truncateString(inputStr, 4096))

	step := &Step{
		span:      span,
		session:   s,
		stepType:  StepTypeTool,
		startTime: time.Now(),
	}

	// Execute the function
	fn(step)

	// End the step
	step.end()
}

// HumanStep creates a human-in-the-loop step span and executes the given function.
// This is used when the agent needs to wait for or receive human input.
//
// Usage:
//
//	session.HumanStep(ctx, "approval", func(step *agent.Step) {
//	    step.SetReasoning("Need user confirmation before booking")
//	    approved := waitForUserApproval()
//	    if approved {
//	        step.SetHumanAction(agent.HumanActionApproved)
//	    } else {
//	        step.SetHumanAction(agent.HumanActionRejected)
//	    }
//	})
func (s *Session) HumanStep(ctx context.Context, name string, fn func(*Step)) {
	stepIndex := s.nextStepIndex()
	s.incrementHumanInteractions()

	// Update session status to waiting
	s.SetStatus(StatusWaitingHuman)

	// Create span name following convention: agent.{agent_name}.human.{action}
	spanName := fmt.Sprintf("agent.%s.human.%s", s.config.Name, name)

	ctx, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "internal"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeHuman))
	span.SetAttribute(AttrStepName, name)

	step := &Step{
		span:      span,
		session:   s,
		stepType:  StepTypeHuman,
		startTime: time.Now(),
	}

	// Execute the function
	fn(step)

	// Restore session status to running
	s.SetStatus(StatusRunning)

	// End the step
	step.end()
}

// Handoff creates a handoff span for delegating to another agent.
// Returns the new session ID that should be used by the target agent.
//
// Usage:
//
//	newSessionID := session.Handoff(ctx, "booking-agent", "Specialized for flight bookings")
func (s *Session) Handoff(ctx context.Context, targetAgent string, reason string) string {
	stepIndex := s.nextStepIndex()

	// Generate a new session ID for the target agent
	newSessionID := generateSessionID()

	// Create span name following convention: agent.{agent_name}.handoff.{target}
	spanName := fmt.Sprintf("agent.%s.handoff.%s", s.config.Name, targetAgent)

	_, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "internal"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeHandoff))
	span.SetAttribute(AttrStepName, fmt.Sprintf("handoff to %s", targetAgent))

	// Set handoff attributes
	span.SetAttribute(AttrHandoffTo, targetAgent)
	span.SetAttribute(AttrHandoffReason, reason)
	span.SetAttribute(AttrHandoffSessionID, newSessionID)

	// End the span immediately (handoffs are instantaneous)
	span.End()

	return newSessionID
}

// HandoffWithContext creates a handoff span with additional context being passed.
//
// Usage:
//
//	newSessionID := session.HandoffWithContext(ctx, "booking-agent",
//	    "Specialized for flight bookings",
//	    "Selected flight: KL1234, Price: $299")
func (s *Session) HandoffWithContext(ctx context.Context, targetAgent, reason, handoffContext string) string {
	stepIndex := s.nextStepIndex()

	// Generate a new session ID for the target agent
	newSessionID := generateSessionID()

	// Create span name following convention: agent.{agent_name}.handoff.{target}
	spanName := fmt.Sprintf("agent.%s.handoff.%s", s.config.Name, targetAgent)

	_, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "internal"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeHandoff))
	span.SetAttribute(AttrStepName, fmt.Sprintf("handoff to %s", targetAgent))

	// Set handoff attributes
	span.SetAttribute(AttrHandoffTo, targetAgent)
	span.SetAttribute(AttrHandoffReason, reason)
	span.SetAttribute(AttrHandoffContext, handoffContext)
	span.SetAttribute(AttrHandoffSessionID, newSessionID)

	// End the span immediately (handoffs are instantaneous)
	span.End()

	return newSessionID
}

// ReasoningStep creates a reasoning step span for internal agent reasoning.
// This is for agent "thinking" that doesn't involve external calls.
//
// Usage:
//
//	session.ReasoningStep(ctx, "comparing_options", func(step *agent.Step) {
//	    step.SetReasoning("Comparing 5 flight options based on price and duration")
//	    // ... internal processing ...
//	})
func (s *Session) ReasoningStep(ctx context.Context, name string, fn func(*Step)) {
	stepIndex := s.nextStepIndex()

	// Create span name
	spanName := fmt.Sprintf("agent.%s.reasoning.%s", s.config.Name, name)

	ctx, span := s.client.StartSpan(s.ctx, spanName, imprint.SpanOptions{Kind: "internal"})

	// Set step attributes
	span.SetAttribute(AttrStepIndex, stepIndex)
	span.SetAttribute(AttrStepType, string(StepTypeReasoning))
	span.SetAttribute(AttrStepName, name)

	step := &Step{
		span:      span,
		session:   s,
		stepType:  StepTypeReasoning,
		startTime: time.Now(),
	}

	// Execute the function
	fn(step)

	// End the step
	step.end()
}
