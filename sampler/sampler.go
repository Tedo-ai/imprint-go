// Package sampler provides trace sampling strategies for Imprint.
//
// Sampling reduces the volume of trace data sent to the backend while
// maintaining statistical significance and ensuring errors are always captured.
//
// The package implements:
// - Head-based sampling: Decision made at trace creation time
// - Consistent sampling: Same trace ID always gets same decision across services
// - Error promotion: Unsampled traces are promoted if errors occur
package sampler

import (
	"encoding/binary"
	"encoding/hex"
	"hash/fnv"
)

// Sampler determines whether a trace should be sampled.
type Sampler interface {
	// ShouldSample returns true if the trace should be sampled.
	// The traceID is used for consistent sampling decisions.
	ShouldSample(traceID string) bool
}

// RateSampler implements head-based probabilistic sampling.
// It uses trace ID hashing for consistent decisions across services.
type RateSampler struct {
	rate      float64
	threshold uint64
}

// NewRateSampler creates a sampler with the given rate (0.0 to 1.0).
// A rate of 1.0 samples all traces, 0.0 samples none.
// The sampling decision is deterministic based on trace ID.
func NewRateSampler(rate float64) *RateSampler {
	if rate <= 0 {
		return &RateSampler{rate: 0, threshold: 0}
	}
	if rate >= 1 {
		return &RateSampler{rate: 1, threshold: ^uint64(0)}
	}

	// Convert rate to a threshold for uint64 comparison
	// This gives us fine-grained control (1 in 18 quintillion resolution)
	threshold := uint64(float64(^uint64(0)) * rate)

	return &RateSampler{
		rate:      rate,
		threshold: threshold,
	}
}

// ShouldSample returns true if the trace should be sampled.
// The decision is consistent for a given trace ID.
func (s *RateSampler) ShouldSample(traceID string) bool {
	if s.rate >= 1 {
		return true
	}
	if s.rate <= 0 {
		return false
	}

	// Hash the trace ID for consistent sampling
	hash := hashTraceID(traceID)
	return hash < s.threshold
}

// Rate returns the configured sampling rate.
func (s *RateSampler) Rate() float64 {
	return s.rate
}

// hashTraceID creates a consistent hash from a trace ID.
// This ensures the same trace ID always produces the same sampling decision,
// which is crucial for distributed tracing across services.
func hashTraceID(traceID string) uint64 {
	// Try to parse as hex first (standard trace IDs)
	if len(traceID) >= 16 {
		// Use the last 8 bytes of the trace ID if it's hex
		if b, err := hex.DecodeString(traceID[len(traceID)-16:]); err == nil && len(b) == 8 {
			return binary.BigEndian.Uint64(b)
		}
	}

	// Fall back to FNV hash for non-standard trace IDs
	h := fnv.New64a()
	h.Write([]byte(traceID))
	return h.Sum64()
}

// AlwaysSample is a sampler that always samples.
type AlwaysSample struct{}

// ShouldSample always returns true.
func (AlwaysSample) ShouldSample(string) bool {
	return true
}

// NeverSample is a sampler that never samples.
type NeverSample struct{}

// ShouldSample always returns false.
func (NeverSample) ShouldSample(string) bool {
	return false
}

// ParentBasedSampler defers to the parent span's sampling decision.
// If there's no parent, it uses a fallback sampler.
type ParentBasedSampler struct {
	fallback Sampler
}

// NewParentBasedSampler creates a sampler that respects parent decisions.
func NewParentBasedSampler(fallback Sampler) *ParentBasedSampler {
	if fallback == nil {
		fallback = AlwaysSample{}
	}
	return &ParentBasedSampler{fallback: fallback}
}

// ShouldSample defers to the parent or uses fallback.
// Note: This is called with just the trace ID; parent sampling status
// must be tracked separately by the caller.
func (s *ParentBasedSampler) ShouldSample(traceID string) bool {
	return s.fallback.ShouldSample(traceID)
}

// Fallback returns the fallback sampler for root spans.
func (s *ParentBasedSampler) Fallback() Sampler {
	return s.fallback
}

// CompositeSampler allows combining multiple sampling strategies.
type CompositeSampler struct {
	samplers []Sampler
	mode     CompositeMode
}

// CompositeMode determines how multiple samplers are combined.
type CompositeMode int

const (
	// CompositeAND requires all samplers to agree (intersection).
	CompositeAND CompositeMode = iota
	// CompositeOR samples if any sampler agrees (union).
	CompositeOR
)

// NewCompositeSampler creates a sampler from multiple samplers.
func NewCompositeSampler(mode CompositeMode, samplers ...Sampler) *CompositeSampler {
	return &CompositeSampler{
		samplers: samplers,
		mode:     mode,
	}
}

// ShouldSample evaluates all samplers according to the mode.
func (s *CompositeSampler) ShouldSample(traceID string) bool {
	if len(s.samplers) == 0 {
		return true
	}

	switch s.mode {
	case CompositeAND:
		for _, sampler := range s.samplers {
			if !sampler.ShouldSample(traceID) {
				return false
			}
		}
		return true
	case CompositeOR:
		for _, sampler := range s.samplers {
			if sampler.ShouldSample(traceID) {
				return true
			}
		}
		return false
	default:
		return true
	}
}
