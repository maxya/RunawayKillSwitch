package core

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the complete application configuration loaded from killswitch.yaml.
type Config struct {
	Limits        LimitsConfig        `yaml:"limits"`
	PricingMatrix PricingMatrixConfig `yaml:"pricing_matrix"`
	Notifications NotificationConfig  `yaml:"notifications"`
	Routing       RoutingConfig       `yaml:"routing"`
}

// LimitsConfig defines circuit breaker thresholds.
type LimitsConfig struct {
	MaxSpendPerMinuteUSD           float64 `yaml:"max_spend_per_minute_usd"`
	MaxSpendPerHourUSD             float64 `yaml:"max_spend_per_hour_usd"`
	MaxConsecutiveIdenticalPrompts int     `yaml:"max_consecutive_identical_prompts"`
}

// PricingMatrixConfig maps model names to per-million-token costs.
type PricingMatrixConfig struct {
	DefaultInputCostPerM  float64                 `yaml:"default_input_cost_per_m"`
	DefaultOutputCostPerM float64                 `yaml:"default_output_cost_per_m"`
	Models                map[string]ModelPricing `yaml:"models"`
}

// ModelPricing holds input and output costs per million tokens for a specific model.
type ModelPricing struct {
	InputCostPerM  float64 `yaml:"input_cost_per_m"`
	OutputCostPerM float64 `yaml:"output_cost_per_m"`
}

// NotificationConfig controls alert delivery when the circuit breaker trips.
type NotificationConfig struct {
	SystemBell bool          `yaml:"system_bell"`
	Webhook    WebhookConfig `yaml:"webhook"`
}

// WebhookConfig holds external notification endpoint settings.
type WebhookConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	Format  string `yaml:"format"`
}

// RoutingConfig maps model names to upstream provider base URLs.
type RoutingConfig struct {
	DefaultOpenAIProvider string                    `yaml:"default_openai_provider"`
	Providers             map[string]ProviderConfig `yaml:"providers"`
}

// ProviderConfig holds a single upstream provider's base URL.
type ProviderConfig struct {
	BaseURL string `yaml:"base_url"`
}

// LoadConfig reads the YAML file at path and returns a validated Config.
// Zero-value fields are replaced with safe defaults.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}
	if cfg.PricingMatrix.DefaultInputCostPerM == 0 {
		cfg.PricingMatrix.DefaultInputCostPerM = 3.00
	}
	if cfg.PricingMatrix.DefaultOutputCostPerM == 0 {
		cfg.PricingMatrix.DefaultOutputCostPerM = 15.00
	}
	if cfg.Limits.MaxConsecutiveIdenticalPrompts == 0 {
		cfg.Limits.MaxConsecutiveIdenticalPrompts = 4
	}
	// 0.0 means "not set"; use safe defaults to prevent hair-trigger trips on blank config.
	if cfg.Limits.MaxSpendPerMinuteUSD == 0 {
		cfg.Limits.MaxSpendPerMinuteUSD = 5.00
	}
	if cfg.Limits.MaxSpendPerHourUSD == 0 {
		cfg.Limits.MaxSpendPerHourUSD = 20.00
	}
	return &cfg, nil
}

// ModelPricing returns pricing for the given model name.
// Tries exact match first, then longest-prefix match, then falls back to defaults.
func (c *Config) ModelPricing(model string) ModelPricing {
	if pricing, ok := c.PricingMatrix.Models[model]; ok {
		return pricing
	}
	// Prefix fallback — sort longest key first to prevent a short key eating a longer match
	// (e.g., "claude-sonnet" must not match before "claude-sonnet-4-6").
	keys := make([]string, 0, len(c.PricingMatrix.Models))
	for k := range c.PricingMatrix.Models {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return len(keys[i]) > len(keys[j]) })
	for _, key := range keys {
		if strings.HasPrefix(model, key) {
			return c.PricingMatrix.Models[key]
		}
	}
	return ModelPricing{
		InputCostPerM:  c.PricingMatrix.DefaultInputCostPerM,
		OutputCostPerM: c.PricingMatrix.DefaultOutputCostPerM,
	}
}
