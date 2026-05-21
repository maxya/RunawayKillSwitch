package core

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	lockKey           = "agent_state:locked"
	lockReasonKey     = "agent_state:lock_reason"
	promptHistoryKey  = "agent_prompts:history"
	spendMinutePrefix = "spend:minute:"
	spendHourPrefix   = "spend:hour:"
	totalSpendKey     = "spend:total"
	requestCountKey   = "metrics:request_count"
	lastModelKey      = "metrics:last_model"
)

// RedisMetricsStore persists spend counters, lock state, and prompt history to Redis.
type RedisMetricsStore struct {
	client *redis.Client
	config *Config
}

// NewRedisMetricsStore connects to Redis at redisURL and verifies connectivity
// with a ping. Returns an error if the URL is invalid or the ping times out.
func NewRedisMetricsStore(redisURL string, config *Config) (*RedisMetricsStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid redis URL: %w", err)
	}
	client := redis.NewClient(opts)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping failed: %w", err)
	}
	return &RedisMetricsStore{client: client, config: config}, nil
}

// IsLocked returns whether the circuit breaker is currently active and the reason it was tripped.
func (s *RedisMetricsStore) IsLocked(ctx context.Context) (locked bool, reason string, err error) {
	val, err := s.client.Get(ctx, lockKey).Result()
	if err == redis.Nil {
		return false, "", nil
	}
	if err != nil {
		return false, "", err
	}
	reason, _ = s.client.Get(ctx, lockReasonKey).Result()
	return val == "1", reason, nil
}

// TriggerCircuitBreaker sets the lock flag and reason atomically via a Redis pipeline.
func (s *RedisMetricsStore) TriggerCircuitBreaker(ctx context.Context, reason string) error {
	pipe := s.client.Pipeline()
	pipe.Set(ctx, lockKey, "1", 0)
	pipe.Set(ctx, lockReasonKey, reason, 0)
	_, err := pipe.Exec(ctx)
	return err
}

// ResetCircuitBreaker clears the lock, reason, and prompt history. Spend counters are preserved.
func (s *RedisMetricsStore) ResetCircuitBreaker(ctx context.Context) error {
	pipe := s.client.Pipeline()
	pipe.Del(ctx, lockKey)
	pipe.Del(ctx, lockReasonKey)
	pipe.Del(ctx, promptHistoryKey)
	_, err := pipe.Exec(ctx)
	return err
}

// RecordSpend stores cost atomically using integer microdollars (cost * 1_000_000)
// to enable atomic INCRBY operations without floating-point race conditions.
func (s *RedisMetricsStore) RecordSpend(ctx context.Context, model string, inputTokens, outputTokens int64) (float64, error) {
	pricing := s.config.ModelPricing(model)
	cost := (float64(inputTokens)*pricing.InputCostPerM + float64(outputTokens)*pricing.OutputCostPerM) / 1_000_000
	costMicro := int64(cost * 1_000_000)

	now := time.Now()
	minuteKey := spendMinutePrefix + now.Format("200601021504")
	hourKey := spendHourPrefix + now.Format("2006010215")

	pipe := s.client.Pipeline()
	pipe.IncrBy(ctx, minuteKey, costMicro)
	pipe.Expire(ctx, minuteKey, 10*time.Minute)
	pipe.IncrBy(ctx, hourKey, costMicro)
	pipe.Expire(ctx, hourKey, 2*time.Hour)
	pipe.IncrByFloat(ctx, totalSpendKey, cost)
	pipe.Incr(ctx, requestCountKey)
	pipe.Set(ctx, lastModelKey, model, 0)
	_, err := pipe.Exec(ctx)
	return cost, err
}

// GetSlidingWindowSpend returns the total spend over the last N minutes by summing minute buckets.
func (s *RedisMetricsStore) GetSlidingWindowSpend(ctx context.Context, windowMinutes int) (float64, error) {
	now := time.Now()
	keys := make([]string, windowMinutes)
	for i := range keys {
		keys[i] = spendMinutePrefix + now.Add(time.Duration(-i)*time.Minute).Format("200601021504")
	}
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, err
	}
	var totalMicro int64
	for _, v := range vals {
		if v == nil {
			continue
		}
		n, _ := strconv.ParseInt(v.(string), 10, 64)
		totalMicro += n
	}
	return float64(totalMicro) / 1_000_000, nil
}

// GetHourlySpend returns the total spend over the current and previous hour buckets.
func (s *RedisMetricsStore) GetHourlySpend(ctx context.Context) (float64, error) {
	now := time.Now()
	keys := []string{
		spendHourPrefix + now.Format("2006010215"),
		spendHourPrefix + now.Add(-time.Hour).Format("2006010215"),
	}
	vals, err := s.client.MGet(ctx, keys...).Result()
	if err != nil {
		return 0, err
	}
	var totalMicro int64
	for _, v := range vals {
		if v == nil {
			continue
		}
		n, _ := strconv.ParseInt(v.(string), 10, 64)
		totalMicro += n
	}
	return float64(totalMicro) / 1_000_000, nil
}

// CheckAndPushPromptHash pushes the hash to Redis history list and returns true
// if the last maxIdentical entries are all the same hash (loop detected).
func (s *RedisMetricsStore) CheckAndPushPromptHash(ctx context.Context, hash string, maxIdentical int) (bool, error) {
	pipe := s.client.Pipeline()
	pipe.LPush(ctx, promptHistoryKey, hash)
	pipe.LTrim(ctx, promptHistoryKey, 0, int64(maxIdentical*2-1))
	rangeCmd := pipe.LRange(ctx, promptHistoryKey, 0, int64(maxIdentical-1))
	if _, err := pipe.Exec(ctx); err != nil {
		return false, err
	}
	hashes := rangeCmd.Val()
	if len(hashes) < maxIdentical {
		return false, nil
	}
	for _, h := range hashes[1:] {
		if h != hashes[0] {
			return false, nil
		}
	}
	return true, nil
}

// MetricsSummary is a point-in-time snapshot of proxy metrics for the admin dashboard.
type MetricsSummary struct {
	Locked         bool    `json:"locked"`
	LockReason     string  `json:"lock_reason"`
	SpendLast1Min  float64 `json:"spend_last_1min"`
	SpendLast5Min  float64 `json:"spend_last_5min"`
	SpendLastHour  float64 `json:"spend_last_hour"`
	SpendTotal     float64 `json:"spend_total"`
	RequestCount   int64   `json:"request_count"`
	LastModel      string  `json:"last_model"`
	LimitPerMinute float64 `json:"limit_per_minute"`
	LimitPerHour   float64 `json:"limit_per_hour"`
}

// GetMetricsSummary returns a complete snapshot of current metrics for the dashboard and API.
// Spend query errors are logged and treated as zero (fail open) so the dashboard always renders;
// only a lock-check failure is fatal because it controls request blocking decisions.
func (s *RedisMetricsStore) GetMetricsSummary(ctx context.Context) (*MetricsSummary, error) {
	minuteSpend, err := s.GetSlidingWindowSpend(ctx, 2)
	if err != nil {
		slog.Warn("metrics: minute spend unavailable", "error", err)
	}
	fiveMinSpend, err := s.GetSlidingWindowSpend(ctx, 5)
	if err != nil {
		slog.Warn("metrics: 5min spend unavailable", "error", err)
	}
	hourSpend, err := s.GetHourlySpend(ctx)
	if err != nil {
		slog.Warn("metrics: hourly spend unavailable", "error", err)
	}
	totalSpend, err := s.client.Get(ctx, totalSpendKey).Float64()
	if err != nil && err != redis.Nil {
		slog.Warn("metrics: total spend unavailable", "error", err)
	}
	reqCount, err := s.client.Get(ctx, requestCountKey).Int64()
	if err != nil && err != redis.Nil {
		slog.Warn("metrics: request count unavailable", "error", err)
	}
	lastModel, err := s.client.Get(ctx, lastModelKey).Result()
	if err != nil && err != redis.Nil {
		slog.Warn("metrics: last model unavailable", "error", err)
	}
	locked, reason, err := s.IsLocked(ctx)
	if err != nil {
		return nil, err
	}
	return &MetricsSummary{
		Locked:         locked,
		LockReason:     reason,
		SpendLast1Min:  minuteSpend,
		SpendLast5Min:  fiveMinSpend,
		SpendLastHour:  hourSpend,
		SpendTotal:     totalSpend,
		RequestCount:   reqCount,
		LastModel:      lastModel,
		LimitPerMinute: s.config.Limits.MaxSpendPerMinuteUSD,
		LimitPerHour:   s.config.Limits.MaxSpendPerHourUSD,
	}, nil
}
