// Package agent provides semantic conventions for AI agent observability.
// It implements the Agent Trace Standard for tracing multi-step agent workflows
// with LLM calls, tool use, human-in-the-loop, and agent-to-agent handoffs.
package agent

// Semantic attribute keys for agent sessions.
// These follow the Agent Trace Standard specification.
const (
	// Session attributes
	AttrSessionID     = "agent.session.id"
	AttrSessionGoal   = "agent.session.goal"
	AttrSessionStatus = "agent.session.status"
	AttrSessionTrigger = "agent.session.trigger"

	// Agent identity attributes
	AttrAgentName        = "agent.name"
	AttrAgentVersion     = "agent.version"
	AttrAgentFramework   = "agent.framework"
	AttrAgentDescription = "agent.description"

	// Step attributes
	AttrStepIndex     = "agent.step.index"
	AttrStepType      = "agent.step.type"
	AttrStepName      = "agent.step.name"
	AttrStepReasoning = "agent.step.reasoning"
	AttrStepStatus    = "agent.step.status"

	// Tool call attributes
	AttrToolName        = "agent.tool.name"
	AttrToolDescription = "agent.tool.description"
	AttrToolInput       = "agent.tool.input"
	AttrToolOutput      = "agent.tool.output"
	AttrToolStatus      = "agent.tool.status"
	AttrToolError       = "agent.tool.error"
	AttrToolRetries     = "agent.tool.retries"

	// Human-in-the-loop attributes
	AttrHumanAction   = "agent.human.action"
	AttrHumanFeedback = "agent.human.feedback"
	AttrHumanWaitMS   = "agent.human.wait_ms"
	AttrHumanUserID   = "agent.human.user_id"

	// Handoff attributes
	AttrHandoffTo        = "agent.handoff.to"
	AttrHandoffReason    = "agent.handoff.reason"
	AttrHandoffContext   = "agent.handoff.context"
	AttrHandoffSessionID = "agent.handoff.session_id"

	// Cost & token rollup attributes (set on session span)
	AttrTotalCostUSD         = "agent.session.total_cost_usd"
	AttrTotalTokensIn        = "agent.session.total_tokens_in"
	AttrTotalTokensOut       = "agent.session.total_tokens_out"
	AttrLLMCalls             = "agent.session.llm_calls"
	AttrToolCalls            = "agent.session.tool_calls"
	AttrHumanInteractions    = "agent.session.human_interactions"
)

// SessionStatus represents the status of an agent session.
type SessionStatus string

const (
	StatusRunning      SessionStatus = "running"
	StatusCompleted    SessionStatus = "completed"
	StatusFailed       SessionStatus = "failed"
	StatusWaitingHuman SessionStatus = "waiting_human"
)

// SessionTrigger represents what initiated the session.
type SessionTrigger string

const (
	TriggerUserMessage SessionTrigger = "user_message"
	TriggerScheduled   SessionTrigger = "scheduled"
	TriggerWebhook     SessionTrigger = "webhook"
	TriggerAgentHandoff SessionTrigger = "agent_handoff"
)

// StepType represents the type of agent step.
type StepType string

const (
	StepTypeLLM       StepType = "llm"
	StepTypeTool      StepType = "tool"
	StepTypeHuman     StepType = "human"
	StepTypeHandoff   StepType = "handoff"
	StepTypeReasoning StepType = "reasoning"
)

// StepStatus represents the outcome of a step.
type StepStatus string

const (
	StepStatusSuccess StepStatus = "success"
	StepStatusFailed  StepStatus = "failed"
	StepStatusSkipped StepStatus = "skipped"
	StepStatusRetried StepStatus = "retried"
)

// ToolStatus represents the execution status of a tool.
type ToolStatus string

const (
	ToolStatusSuccess ToolStatus = "success"
	ToolStatusError   ToolStatus = "error"
	ToolStatusTimeout ToolStatus = "timeout"
)

// HumanAction represents what the human did.
type HumanAction string

const (
	HumanActionApproved HumanAction = "approved"
	HumanActionRejected HumanAction = "rejected"
	HumanActionModified HumanAction = "modified"
	HumanActionTimeout  HumanAction = "timeout"
)
