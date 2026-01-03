package agent

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/tedo-ai/imprint-go"
)

// Step represents an individual step within an agent session.
// Steps are created by calling LLMStep, ToolStep, HumanStep, or Handoff on a Session.
type Step struct {
	span      *imprint.Span
	session   *Session
	stepType  StepType
	startTime time.Time

	mu        sync.Mutex
	status    StepStatus
	reasoning string

	// LLM-specific fields
	tokensIn  int64
	tokensOut int64
	costUSD   float64

	// Tool-specific fields
	toolOutput string
	toolError  string

	// Human-specific fields
	humanAction   HumanAction
	humanFeedback string
	humanWaitMS   int64
}

// SetReasoning sets the reasoning for why this step was taken.
// This is useful for debugging agent decision-making.
func (s *Step) SetReasoning(reasoning string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reasoning = reasoning
	s.span.SetAttribute(AttrStepReasoning, reasoning)
}

// LLMResponse contains the response data from an LLM call.
type LLMResponse struct {
	// TokensIn is the number of input tokens
	TokensIn int64

	// TokensOut is the number of output tokens
	TokensOut int64

	// CostUSD is the cost in USD for this call
	CostUSD float64

	// Model is the model used (optional, for overriding the session default)
	Model string

	// Provider is the LLM provider (optional)
	Provider string
}

// SetLLMResponse records the response from an LLM call.
// This automatically updates the session rollup statistics.
func (s *Step) SetLLMResponse(response LLMResponse) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.tokensIn = response.TokensIn
	s.tokensOut = response.TokensOut
	s.costUSD = response.CostUSD

	// Set LLM attributes on the span
	s.span.SetAttribute("llm.tokens_input", response.TokensIn)
	s.span.SetAttribute("llm.tokens_output", response.TokensOut)
	s.span.SetAttribute("llm.tokens_total", response.TokensIn+response.TokensOut)
	s.span.SetAttribute("llm.cost_usd", response.CostUSD)

	if response.Model != "" {
		s.span.SetAttribute("llm.model", response.Model)
	}
	if response.Provider != "" {
		s.span.SetAttribute("llm.system", response.Provider)
	}

	// Update session rollup
	s.session.addLLMStats(response.TokensIn, response.TokensOut, response.CostUSD)
}

// SetOutput sets the output of a tool step.
// The output is JSON-encoded if it's not already a string.
func (s *Step) SetOutput(output interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var outputStr string
	switch v := output.(type) {
	case string:
		outputStr = v
	case []byte:
		outputStr = string(v)
	default:
		data, err := json.Marshal(v)
		if err != nil {
			outputStr = "<error encoding output>"
		} else {
			outputStr = string(data)
		}
	}

	s.toolOutput = outputStr
	s.span.SetAttribute(AttrToolOutput, truncateString(outputStr, 4096))
	s.span.SetAttribute(AttrToolStatus, string(ToolStatusSuccess))
	s.status = StepStatusSuccess
}

// SetError records an error on the step.
// This marks the step as failed and records the error message.
func (s *Step) SetError(err error) {
	if err == nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	s.status = StepStatusFailed
	s.toolError = err.Error()

	s.span.SetAttribute(AttrStepStatus, string(StepStatusFailed))
	s.span.RecordError(err)

	if s.stepType == StepTypeTool {
		s.span.SetAttribute(AttrToolStatus, string(ToolStatusError))
		s.span.SetAttribute(AttrToolError, err.Error())
	}
}

// SetHumanAction records what action the human took.
func (s *Step) SetHumanAction(action HumanAction) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.humanAction = action
	s.span.SetAttribute(AttrHumanAction, string(action))

	// Calculate wait time
	waitMS := time.Since(s.startTime).Milliseconds()
	s.humanWaitMS = waitMS
	s.span.SetAttribute(AttrHumanWaitMS, waitMS)

	// Set status based on action
	switch action {
	case HumanActionApproved, HumanActionModified:
		s.status = StepStatusSuccess
	case HumanActionRejected:
		s.status = StepStatusFailed
	case HumanActionTimeout:
		s.status = StepStatusFailed
	}
	s.span.SetAttribute(AttrStepStatus, string(s.status))
}

// SetHumanFeedback records feedback text from the human.
func (s *Step) SetHumanFeedback(feedback string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.humanFeedback = feedback
	s.span.SetAttribute(AttrHumanFeedback, feedback)
}

// SetHumanUserID records which user responded.
func (s *Step) SetHumanUserID(userID string) {
	s.span.SetAttribute(AttrHumanUserID, userID)
}

// SetToolRetries records the number of retry attempts for a tool call.
func (s *Step) SetToolRetries(retries int) {
	s.span.SetAttribute(AttrToolRetries, retries)
}

// end finalizes the step span.
func (s *Step) end() {
	s.mu.Lock()
	// Set final status if not already set
	if s.status == "" {
		s.status = StepStatusSuccess
	}
	s.span.SetAttribute(AttrStepStatus, string(s.status))
	s.mu.Unlock()

	s.span.End()
}

// truncateString truncates a string to the given maximum length.
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
