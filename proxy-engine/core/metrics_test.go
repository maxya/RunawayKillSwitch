//go:build integration

package core_test

import (
	"context"
	"testing"

	"github.com/runaway-killswitch/proxy-engine/core"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

func startRedis(t *testing.T) (redisURL string, cleanup func()) {
	t.Helper()
	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "redis:7.2-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}
	c, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redis: %v", err)
	}
	host, err := c.Host(ctx)
	if err != nil {
		t.Fatalf("get host: %v", err)
	}
	port, err := c.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("get port: %v", err)
	}
	return "redis://" + host + ":" + port.Port() + "/0", func() { c.Terminate(ctx) }
}

func TestIsLockedFreshRedis(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	locked, reason, err := store.IsLocked(ctx)
	if err != nil {
		t.Fatalf("IsLocked() error = %v", err)
	}
	if locked {
		t.Error("expected locked=false on fresh Redis")
	}
	if reason != "" {
		t.Errorf("expected empty reason, got %q", reason)
	}
}

func TestTriggerCircuitBreakerThenIsLocked(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := store.TriggerCircuitBreaker(ctx, "test trip reason"); err != nil {
		t.Fatalf("TriggerCircuitBreaker() error = %v", err)
	}

	locked, reason, err := store.IsLocked(ctx)
	if err != nil {
		t.Fatalf("IsLocked() error = %v", err)
	}
	if !locked {
		t.Error("expected locked=true after trigger")
	}
	if reason != "test trip reason" {
		t.Errorf("reason = %q, want %q", reason, "test trip reason")
	}
}

func TestResetCircuitBreakerClearsLockAndHistory(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00, MaxConsecutiveIdenticalPrompts: 3},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	if err := store.TriggerCircuitBreaker(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, _ = store.CheckAndPushPromptHash(ctx, "hash123", 3)
	}

	locked, _, err := store.IsLocked(ctx)
	if err != nil || !locked {
		t.Fatal("expected lock to be set before reset")
	}

	if err := store.ResetCircuitBreaker(ctx); err != nil {
		t.Fatalf("ResetCircuitBreaker() error = %v", err)
	}

	locked, reason, err := store.IsLocked(ctx)
	if err != nil {
		t.Fatalf("IsLocked() error = %v", err)
	}
	if locked {
		t.Error("expected locked=false after reset")
	}
	if reason != "" {
		t.Errorf("expected empty reason after reset, got %q", reason)
	}

	loopDetected, err := store.CheckAndPushPromptHash(ctx, "newhash", 3)
	if err != nil {
		t.Fatal(err)
	}
	if loopDetected {
		t.Error("expected no loop detection after reset cleared history")
	}
}

func TestCheckAndPushPromptHash(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxConsecutiveIdenticalPrompts: 3},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	const maxIdentical = 3

	// Push N-1 identical hashes — must not trigger.
	for i := 0; i < maxIdentical-1; i++ {
		detected, err := store.CheckAndPushPromptHash(ctx, "same-hash", maxIdentical)
		if err != nil {
			t.Fatal(err)
		}
		if detected {
			t.Errorf("call %d: expected no loop detection with fewer than %d identical hashes", i+1, maxIdentical)
		}
	}

	// The Nth identical hash must trigger loop detection.
	detected, err := store.CheckAndPushPromptHash(ctx, "same-hash", maxIdentical)
	if err != nil {
		t.Fatal(err)
	}
	if !detected {
		t.Error("expected loop detection after N identical hashes")
	}

	// A different hash immediately after N identical must not trigger.
	detected, err = store.CheckAndPushPromptHash(ctx, "different-hash", maxIdentical)
	if err != nil {
		t.Fatal(err)
	}
	if detected {
		t.Error("expected no loop detection after a different hash breaks the streak")
	}
}

func TestRecordSpendAccumulates(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.00,
			DefaultOutputCostPerM: 15.00,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	cost1, err := store.RecordSpend(ctx, "unknown", 1000, 500)
	if err != nil {
		t.Fatal(err)
	}
	cost2, err := store.RecordSpend(ctx, "unknown", 2000, 1000)
	if err != nil {
		t.Fatal(err)
	}

	spend, err := store.GetSlidingWindowSpend(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := cost1 + cost2
	if spend < want-0.0001 || spend > want+0.0001 {
		t.Errorf("GetSlidingWindowSpend = %.6f, want %.6f", spend, want)
	}
}

func TestRecordSpendIncrementsRequestCount(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.00,
			DefaultOutputCostPerM: 15.00,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	_, err = store.RecordSpend(ctx, "gpt-4o", 100, 50)
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.RecordSpend(ctx, "gpt-4o", 200, 100)
	if err != nil {
		t.Fatal(err)
	}

	summary, err := store.GetMetricsSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.RequestCount != 2 {
		t.Errorf("RequestCount = %d, want 2", summary.RequestCount)
	}
	if summary.LastModel != "gpt-4o" {
		t.Errorf("LastModel = %q, want %q", summary.LastModel, "gpt-4o")
	}
}

func TestGetMetricsSummary(t *testing.T) {
	redisURL, cleanup := startRedis(t)
	defer cleanup()

	cfg := &core.Config{
		Limits: core.LimitsConfig{MaxSpendPerMinuteUSD: 5.00, MaxSpendPerHourUSD: 20.00},
		PricingMatrix: core.PricingMatrixConfig{
			DefaultInputCostPerM:  3.00,
			DefaultOutputCostPerM: 15.00,
		},
	}
	store, err := core.NewRedisMetricsStore(redisURL, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	summary, err := store.GetMetricsSummary(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if summary.Locked {
		t.Error("expected Locked=false on fresh Redis")
	}
	if summary.LimitPerMinute != 5.00 {
		t.Errorf("LimitPerMinute = %v, want 5.00", summary.LimitPerMinute)
	}
	if summary.LimitPerHour != 20.00 {
		t.Errorf("LimitPerHour = %v, want 20.00", summary.LimitPerHour)
	}
	if summary.RequestCount != 0 {
		t.Errorf("RequestCount = %d, want 0", summary.RequestCount)
	}
}
