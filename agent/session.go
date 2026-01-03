package agent

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tedo-ai/imprint-go"
)

// SessionConfig holds configuration for starting an agent session.
type SessionConfig struct {
	// Name is the name of the agent (e.g., "travel-assistant")
	Name string

	// Goal is the human-readable goal/task (e.g., "Book flight to Amsterdam")
	Goal string

	// Framework is the agent framework used (e.g., "langchain", "autogen", "custom")
	Framework string

	// Version is the version of the agent (optional)
	Version string

	// Description is what this agent does (optional)
	Description string

	// Trigger is what initiated the session (optional, defaults to "user_message")
	Trigger SessionTrigger
}

// Session represents an agent session - a complete agent execution from goal to completion.
// It groups all spans (LLM calls, tool calls, human interactions) belonging to one logical task.
type Session struct {
	client    *imprint.Client
	span      *imprint.Span
	ctx       context.Context
	config    SessionConfig
	sessionID string
	startTime time.Time

	// Step counter (atomic for thread safety)
	stepIndex atomic.Int32

	// Rollup stats (protected by mutex)
	mu                sync.Mutex
	totalCostUSD      float64
	totalTokensIn     int64
	totalTokensOut    int64
	llmCalls          int32
	toolCalls         int32
	humanInteractions int32
	status            SessionStatus
}

// StartSession creates a new agent session span.
// The session span is the root of all agent activity for this task.
//
// Usage:
//
//	session := agent.StartSession(ctx, client, agent.SessionConfig{
//	    Name:      "travel-assistant",
//	    Goal:      "Book flight to Amsterdam",
//	    Framework: "custom",
//	})
//	defer session.End()
func StartSession(ctx context.Context, client *imprint.Client, config SessionConfig) *Session {
	sessionID := generateSessionID()

	// Set default trigger
	if config.Trigger == "" {
		config.Trigger = TriggerUserMessage
	}

	// Create the session span
	ctx, span := client.StartSpan(ctx, "agent.session", imprint.SpanOptions{Kind: "internal"})

	// Set session attributes
	span.SetAttribute(AttrSessionID, sessionID)
	span.SetAttribute(AttrSessionGoal, config.Goal)
	span.SetAttribute(AttrSessionStatus, string(StatusRunning))
	span.SetAttribute(AttrSessionTrigger, string(config.Trigger))

	// Set agent identity attributes
	span.SetAttribute(AttrAgentName, config.Name)
	if config.Version != "" {
		span.SetAttribute(AttrAgentVersion, config.Version)
	}
	if config.Framework != "" {
		span.SetAttribute(AttrAgentFramework, config.Framework)
	}
	if config.Description != "" {
		span.SetAttribute(AttrAgentDescription, config.Description)
	}

	return &Session{
		client:    client,
		span:      span,
		ctx:       ctx,
		config:    config,
		sessionID: sessionID,
		startTime: time.Now(),
		status:    StatusRunning,
	}
}

// Context returns the context associated with this session.
// Use this context when making calls that should be traced under this session.
func (s *Session) Context() context.Context {
	return s.ctx
}

// SessionID returns the unique identifier for this session.
func (s *Session) SessionID() string {
	return s.sessionID
}

// SetStatus updates the session status.
func (s *Session) SetStatus(status SessionStatus) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
	s.span.SetAttribute(AttrSessionStatus, string(status))
}

// End marks the session as complete and records rollup statistics.
// Call this when the agent has finished processing the goal.
func (s *Session) End() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Set final status if still running
	if s.status == StatusRunning {
		s.status = StatusCompleted
	}
	s.span.SetAttribute(AttrSessionStatus, string(s.status))

	// Record rollup statistics
	s.span.SetAttribute(AttrTotalCostUSD, s.totalCostUSD)
	s.span.SetAttribute(AttrTotalTokensIn, s.totalTokensIn)
	s.span.SetAttribute(AttrTotalTokensOut, s.totalTokensOut)
	s.span.SetAttribute(AttrLLMCalls, s.llmCalls)
	s.span.SetAttribute(AttrToolCalls, s.toolCalls)
	s.span.SetAttribute(AttrHumanInteractions, s.humanInteractions)

	// End the session span
	s.span.End()
}

// nextStepIndex returns the next step index (1-based).
func (s *Session) nextStepIndex() int32 {
	return s.stepIndex.Add(1)
}

// addLLMStats adds LLM usage stats to the session rollup.
func (s *Session) addLLMStats(tokensIn, tokensOut int64, costUSD float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.totalTokensIn += tokensIn
	s.totalTokensOut += tokensOut
	s.totalCostUSD += costUSD
	s.llmCalls++
}

// incrementToolCalls increments the tool call counter.
func (s *Session) incrementToolCalls() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls++
}

// incrementHumanInteractions increments the human interaction counter.
func (s *Session) incrementHumanInteractions() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.humanInteractions++
}

// generateSessionID generates a unique session ID.
func generateSessionID() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "sess_" + hex.EncodeToString(b)
}
