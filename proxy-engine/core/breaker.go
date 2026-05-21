package core

import (
	"context"
	"fmt"
)

// CircuitBreaker evaluates each request against configured spend and loop limits
// and sets the Redis lock when a threshold is exceeded.
type CircuitBreaker struct {
	store  *RedisMetricsStore
	config *Config
}

// NewCircuitBreaker creates a CircuitBreaker backed by the given store and config.
func NewCircuitBreaker(store *RedisMetricsStore, config *Config) *CircuitBreaker {
	return &CircuitBreaker{store: store, config: config}
}

// PreRequestCheck runs before forwarding. Returns blocked=true if request must be rejected.
// Checks: (1) global lock, (2) identical prompt loop detection.
func (cb *CircuitBreaker) PreRequestCheck(ctx context.Context, promptHash string) (blocked bool, reason string, err error) {
	locked, lockReason, err := cb.store.IsLocked(ctx)
	if err != nil {
		return false, "", fmt.Errorf("lock check: %w", err)
	}
	if locked {
		return true, lockReason, nil
	}

	loopDetected, err := cb.store.CheckAndPushPromptHash(ctx, promptHash, cb.config.Limits.MaxConsecutiveIdenticalPrompts)
	if err != nil {
		return false, "", fmt.Errorf("hash check: %w", err)
	}
	if loopDetected {
		reason = fmt.Sprintf("Recursive loop: %d consecutive identical prompts detected", cb.config.Limits.MaxConsecutiveIdenticalPrompts)
		if err := cb.store.TriggerCircuitBreaker(ctx, reason); err != nil {
			return true, reason, fmt.Errorf("set lock: %w", err)
		}
		return true, reason, nil
	}

	return false, "", nil
}

// PostResponseRecord runs after token counts are known. Records spend and checks velocity limits.
// Returns triggered=true if the circuit breaker was activated by this call.
// Note: the current response is already being sent to the client when this runs; the lock
// blocks the NEXT request, not the current one.
func (cb *CircuitBreaker) PostResponseRecord(ctx context.Context, model string, inputTokens, outputTokens int64) (triggered bool, reason string, err error) {
	if inputTokens == 0 && outputTokens == 0 {
		return false, "", nil
	}

	if _, err := cb.store.RecordSpend(ctx, model, inputTokens, outputTokens); err != nil {
		return false, "", fmt.Errorf("record spend: %w", err)
	}

	minuteSpend, err := cb.store.GetSlidingWindowSpend(ctx, 1)
	if err == nil && minuteSpend > cb.config.Limits.MaxSpendPerMinuteUSD {
		reason = fmt.Sprintf("Velocity limit: $%.4f/min exceeds limit of $%.2f/min", minuteSpend, cb.config.Limits.MaxSpendPerMinuteUSD)
		if triggerErr := cb.store.TriggerCircuitBreaker(ctx, reason); triggerErr != nil {
			return false, "", triggerErr
		}
		return true, reason, nil
	}

	hourSpend, err := cb.store.GetHourlySpend(ctx)
	if err == nil && hourSpend > cb.config.Limits.MaxSpendPerHourUSD {
		reason = fmt.Sprintf("Hourly limit: $%.4f exceeds limit of $%.2f", hourSpend, cb.config.Limits.MaxSpendPerHourUSD)
		if triggerErr := cb.store.TriggerCircuitBreaker(ctx, reason); triggerErr != nil {
			return false, "", triggerErr
		}
		return true, reason, nil
	}

	return false, "", nil
}
