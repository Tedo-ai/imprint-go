// Package imprint provides LLM observability helpers for tracing AI/ML model calls.
package imprint

import (
	"context"
	"fmt"
	"strings"
)

// LLMSpanBuilder configures an LLM span before starting it.
// Use LLMSpan to create a builder, chain configuration methods, then call Start.
type LLMSpanBuilder struct {
	ctx      context.Context
	client   *Client
	provider string
	model    string

	// Token counts
	tokensInput  uint32
	tokensOutput uint32
	hasTokens    bool

	// Cost tracking
	costUSD float64
	hasCost bool

	// Prompt template metadata
	promptTemplate string
	promptVersion  string
	hasPrompt      bool

	// Feedback data
	feedbackConfidence float32
	feedbackSource     string
	feedbackFlags      []string
	hasFeedback        bool
}

// LLMSpan creates a new LLM span builder for tracing AI model calls.
// The provider should be the LLM provider name (e.g., "openai", "anthropic", "bedrock").
// The model should be the model identifier (e.g., "gpt-4", "claude-3-opus").
//
// Usage:
//
//	span := client.LLMSpan(ctx, "openai", "gpt-4").
//	    WithTokens(150, 500).
//	    WithCost(0.023).
//	    Start()
//	defer span.End()
func (c *Client) LLMSpan(ctx context.Context, provider, model string) *LLMSpanBuilder {
	return &LLMSpanBuilder{
		ctx:      ctx,
		client:   c,
		provider: provider,
		model:    model,
	}
}

// WithTokens sets the input and output token counts for the LLM call.
func (b *LLMSpanBuilder) WithTokens(input, output uint32) *LLMSpanBuilder {
	b.tokensInput = input
	b.tokensOutput = output
	b.hasTokens = true
	return b
}

// WithCost sets the cost in USD for the LLM call.
func (b *LLMSpanBuilder) WithCost(usd float64) *LLMSpanBuilder {
	b.costUSD = usd
	b.hasCost = true
	return b
}

// WithPromptTemplate sets the prompt template name and version.
// This is useful for tracking which prompts are being used in production.
func (b *LLMSpanBuilder) WithPromptTemplate(name, version string) *LLMSpanBuilder {
	b.promptTemplate = name
	b.promptVersion = version
	b.hasPrompt = true
	return b
}

// WithFeedback sets feedback data for the LLM call.
// confidence is a score from 0.0 to 1.0 indicating model confidence.
// source indicates where the feedback came from (e.g., "user", "automated", "human-review").
// flags are optional labels for categorizing the response (e.g., "hallucination", "off-topic").
func (b *LLMSpanBuilder) WithFeedback(confidence float32, source string, flags []string) *LLMSpanBuilder {
	b.feedbackConfidence = confidence
	b.feedbackSource = source
	b.feedbackFlags = flags
	b.hasFeedback = true
	return b
}

// Start creates and starts the LLM span with all configured attributes.
// Returns the span and a new context containing the span.
// The span should be ended by calling span.End() when the LLM call completes.
func (b *LLMSpanBuilder) Start() (context.Context, *Span) {
	// Create span name from provider and model
	spanName := fmt.Sprintf("llm.%s/%s", b.provider, b.model)

	ctx, span := b.client.StartSpan(b.ctx, spanName, SpanOptions{Kind: "client"})

	// Set LLM-specific attributes following semantic conventions
	span.SetAttribute("llm.system", b.provider)
	span.SetAttribute("llm.model", b.model)

	// Set token counts if provided
	if b.hasTokens {
		span.SetAttribute("llm.tokens_input", b.tokensInput)
		span.SetAttribute("llm.tokens_output", b.tokensOutput)
		// Also set total for convenience
		span.SetAttribute("llm.tokens_total", b.tokensInput+b.tokensOutput)
	}

	// Set cost if provided
	if b.hasCost {
		span.SetAttribute("llm.cost_usd", b.costUSD)
	}

	// Set prompt template metadata if provided
	if b.hasPrompt {
		if b.promptTemplate != "" {
			span.SetAttribute("llm.prompt_template", b.promptTemplate)
		}
		if b.promptVersion != "" {
			span.SetAttribute("llm.prompt_version", b.promptVersion)
		}
	}

	// Set feedback data if provided
	if b.hasFeedback {
		span.SetAttribute("llm.feedback_confidence", b.feedbackConfidence)
		if b.feedbackSource != "" {
			span.SetAttribute("llm.feedback_source", b.feedbackSource)
		}
		if len(b.feedbackFlags) > 0 {
			span.SetAttribute("llm.feedback_flags", strings.Join(b.feedbackFlags, ","))
		}
	}

	return ctx, span
}

// StartSpan is an alias for Start that returns only the span.
// Useful when you don't need the updated context.
//
// Usage:
//
//	span := client.LLMSpan(ctx, "openai", "gpt-4").
//	    WithTokens(150, 500).
//	    StartSpan()
//	defer span.End()
func (b *LLMSpanBuilder) StartSpan() *Span {
	_, span := b.Start()
	return span
}

// SetLLMResponse is a convenience method on Span for adding LLM-specific
// attributes after the span has been started. This is useful when token
// counts and costs are only known after the API call completes.
//
// Usage:
//
//	ctx, span := client.LLMSpan(ctx, "openai", "gpt-4").Start()
//	defer span.End()
//	// ... make API call ...
//	span.SetLLMResponse(150, 500, 0.023)
func (s *Span) SetLLMResponse(tokensInput, tokensOutput uint32, costUSD float64) {
	s.SetAttribute("llm.tokens_input", tokensInput)
	s.SetAttribute("llm.tokens_output", tokensOutput)
	s.SetAttribute("llm.tokens_total", tokensInput+tokensOutput)
	s.SetAttribute("llm.cost_usd", costUSD)
}

// SetLLMFeedback sets feedback data on an existing span.
// This is useful for adding feedback after the LLM call completes.
func (s *Span) SetLLMFeedback(confidence float32, source string, flags []string) {
	s.SetAttribute("llm.feedback_confidence", confidence)
	if source != "" {
		s.SetAttribute("llm.feedback_source", source)
	}
	if len(flags) > 0 {
		s.SetAttribute("llm.feedback_flags", strings.Join(flags, ","))
	}
}
