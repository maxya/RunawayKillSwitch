package core

import (
	"os"

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
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, err
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
	return &cfg, nil
}

// GetModelPricing returns pricing for the given model name, falling back to defaults when not found.
func (c *Config) GetModelPricing(model string) ModelPricing {
	if pricing, ok := c.PricingMatrix.Models[model]; ok {
		return pricing
	}
	return ModelPricing{
		InputCostPerM:  c.PricingMatrix.DefaultInputCostPerM,
		OutputCostPerM: c.PricingMatrix.DefaultOutputCostPerM,
	}
}
