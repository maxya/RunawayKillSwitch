//go:build integration

package core_test

import (
	"context"
	"testing"

	"github.com/runaway-killswitch/proxy-engine/core"
)

func TestPreRequestCheckBlocksWhenLocked(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxConsecutiveIdenticalPrompts: 3},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	breaker := core.NewCircuitBreaker(store, cfg)
	ctx := context.Background()

	if err := store.TriggerCircuitBreaker(ctx, "manual lock"); err != nil {
		t.Fatal(err)
	}

	blocked, reason, err := breaker.PreRequestCheck(ctx, "any-hash")
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked=true when lock is set")
	}
	if reason != "manual lock" {
		t.Errorf("reason = %q, want %q", reason, "manual lock")
	}
}

func TestPreRequestCheckDetectsLoop(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxConsecutiveIdenticalPrompts: 3},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	breaker := core.NewCircuitBreaker(store, cfg)
	ctx := context.Background()
	const hash = "loop-hash-123"

	for i := 0; i < 2; i++ {
		blocked, _, err := breaker.PreRequestCheck(ctx, hash)
		if err != nil {
			t.Fatal(err)
		}
		if blocked {
			t.Errorf("call %d: expected not blocked yet", i+1)
		}
	}

	blocked, reason, err := breaker.PreRequestCheck(ctx, hash)
	if err != nil {
		t.Fatal(err)
	}
	if !blocked {
		t.Error("expected blocked=true after 3 identical prompt hashes")
	}
	if reason == "" {
		t.Error("expected non-empty trip reason for loop detection")
	}
}

func TestPostResponseRecordTripsOnVelocity(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 0.001, MaxSpendPerHourUSD: 20.00},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.0,
			DefaultOutputCostPerM: 15.0,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	breaker := core.NewCircuitBreaker(store, cfg)
	ctx := context.Background()

	triggered, _, err := breaker.PostResponseRecord(ctx, "unknown", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if triggered {
		t.Error("expected no trip on first small request")
	}

	triggered, reason, err := breaker.PostResponseRecord(ctx, "unknown", 10000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !triggered {
		t.Error("expected circuit breaker to trip on velocity limit")
	}
	if reason == "" {
		t.Error("expected non-empty trip reason")
	}

	locked, _, err := store.IsLocked(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !locked {
		t.Error("expected lock to be set after velocity trip")
	}
}

func TestPostResponseRecordTripsOnHourlyLimit(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 999999, MaxSpendPerHourUSD: 0.001},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.0,
			DefaultOutputCostPerM: 15.0,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	breaker := core.NewCircuitBreaker(store, cfg)
	ctx := context.Background()

	triggered, _, err := breaker.PostResponseRecord(ctx, "unknown", 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if triggered {
		t.Error("expected no trip on first small request")
	}

	triggered, reason, err := breaker.PostResponseRecord(ctx, "unknown", 10000, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if !triggered {
		t.Error("expected circuit breaker to trip on hourly limit")
	}
	if reason == "" {
		t.Error("expected non-empty trip reason")
	}
}

func TestPostResponseRecordZeroTokensNoOp(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 0.001, MaxSpendPerHourUSD: 0.001},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.0,
			DefaultOutputCostPerM: 15.0,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	breaker := core.NewCircuitBreaker(store, cfg)
	ctx := context.Background()

	triggered, _, err := breaker.PostResponseRecord(ctx, "unknown", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if triggered {
		t.Error("expected no trip when both token counts are zero")
	}
}

func TestPostResponseRecordUsesModelPricing(t *testing.T) {
	// Each sub-test uses its own Redis container to prevent spend from one
	// test leaking into another (ResetCircuitBreaker preserves spend buckets).

	t.Run("cheap model trips velocity limit", func(t *testing.T) {
		redisURL, cleanup := startRedis(t)
		defer cleanup()

		cfg := &core.Config{
			Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 0.01, MaxSpendPerHourUSD: 20.00},
			PricingMatrix: core.PricingMatrixConfig{
				DefaultInputCostPerM:  3.00,
				DefaultOutputCostPerM: 15.00,
				Models: map[string]core.ModelPricing{
					"cheap-model": {InputCostPerM: 0.10, OutputCostPerM: 0.50},
				},
			},
		}
		store, err := core.NewRedisMetricsStore(redisURL, cfg)
		if err != nil {
			t.Fatal(err)
		}
		breaker := core.NewCircuitBreaker(store, cfg)
		ctx := context.Background()

		// 200k input tokens × $0.10/M = $0.02 > $0.01/min limit
		triggered, _, err := breaker.PostResponseRecord(ctx, "cheap-model", 200000, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !triggered {
			t.Error("expected trip with cheap model at limit boundary")
		}
	})

	t.Run("default pricing trips velocity limit", func(t *testing.T) {
		redisURL, cleanup := startRedis(t)
		defer cleanup()

		cfg := &core.Config{
			Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 0.01, MaxSpendPerHourUSD: 20.00},
			PricingMatrix: core.PricingMatrixConfig{
				DefaultInputCostPerM:  3.00,
				DefaultOutputCostPerM: 15.00,
			},
		}
		store, err := core.NewRedisMetricsStore(redisURL, cfg)
		if err != nil {
			t.Fatal(err)
		}
		breaker := core.NewCircuitBreaker(store, cfg)
		ctx := context.Background()

		// 200k input tokens × $3.00/M = $0.60 > $0.01/min limit
		triggered, _, err := breaker.PostResponseRecord(ctx, "unknown-model", 200000, 0)
		if err != nil {
			t.Fatal(err)
		}
		if !triggered {
			t.Error("expected trip with default pricing at same token count")
		}
	})
}
