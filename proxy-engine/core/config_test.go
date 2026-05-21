package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("valid YAML with all fields", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "config.yaml")
		yaml := `
limits:
  max_spend_per_minute_usd: 2.00
  max_spend_per_hour_usd: 15.00
  max_consecutive_identical_prompts: 3
pricing_matrix:
  default_input_cost_per_m: 5.00
  default_output_cost_per_m: 20.00
  models:
    test-model:
      input_cost_per_m: 1.00
      output_cost_per_m: 3.00
routing:
  default_openai_provider: openai
  providers:
    openai:
      base_url: "https://api.openai.com"
notifications:
  system_bell: true
  webhook:
    enabled: false
    url: ""
    format: "json_summary"
`
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if cfg.Limits.MaxSpendPerMinuteUSD != 2.00 {
			t.Errorf("MaxSpendPerMinuteUSD = %v, want 2.00", cfg.Limits.MaxSpendPerMinuteUSD)
		}
		if cfg.Limits.MaxSpendPerHourUSD != 15.00 {
			t.Errorf("MaxSpendPerHourUSD = %v, want 15.00", cfg.Limits.MaxSpendPerHourUSD)
		}
		if cfg.Limits.MaxConsecutiveIdenticalPrompts != 3 {
			t.Errorf("MaxConsecutiveIdenticalPrompts = %v, want 3", cfg.Limits.MaxConsecutiveIdenticalPrompts)
		}
		if cfg.PricingMatrix.DefaultInputCostPerM != 5.00 {
			t.Errorf("DefaultInputCostPerM = %v, want 5.00", cfg.PricingMatrix.DefaultInputCostPerM)
		}
		if _, ok := cfg.PricingMatrix.Models["test-model"]; !ok {
			t.Error("expected test-model in pricing matrix")
		}
	})

	t.Run("missing file returns error", func(t *testing.T) {
		_, err := LoadConfig("/nonexistent/path/config.yaml")
		if err == nil {
			t.Error("expected error for missing file")
		}
	})

	t.Run("malformed YAML returns error", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "bad.yaml")
		if err := os.WriteFile(path, []byte(`{invalid: yaml: : :`), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := LoadConfig(path)
		if err == nil {
			t.Error("expected error for malformed YAML")
		}
	})

	t.Run("defaults applied when fields are zero", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "minimal.yaml")
		yaml := `
limits: {}
pricing_matrix: {}
`
		if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if cfg.Limits.MaxSpendPerMinuteUSD != 5.00 {
			t.Errorf("MaxSpendPerMinuteUSD default = %v, want 5.00", cfg.Limits.MaxSpendPerMinuteUSD)
		}
		if cfg.Limits.MaxSpendPerHourUSD != 20.00 {
			t.Errorf("MaxSpendPerHourUSD default = %v, want 20.00", cfg.Limits.MaxSpendPerHourUSD)
		}
		if cfg.Limits.MaxConsecutiveIdenticalPrompts != 4 {
			t.Errorf("MaxConsecutiveIdenticalPrompts default = %v, want 4", cfg.Limits.MaxConsecutiveIdenticalPrompts)
		}
		if cfg.PricingMatrix.DefaultInputCostPerM != 3.00 {
			t.Errorf("DefaultInputCostPerM default = %v, want 3.00", cfg.PricingMatrix.DefaultInputCostPerM)
		}
		if cfg.PricingMatrix.DefaultOutputCostPerM != 15.00 {
			t.Errorf("DefaultOutputCostPerM default = %v, want 15.00", cfg.PricingMatrix.DefaultOutputCostPerM)
		}
	})

	t.Run("empty file uses all defaults", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "empty.yaml")
		if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
			t.Fatal(err)
		}

		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig() error = %v", err)
		}
		if cfg.Limits.MaxSpendPerMinuteUSD != 5.00 {
			t.Errorf("MaxSpendPerMinuteUSD = %v, want 5.00", cfg.Limits.MaxSpendPerMinuteUSD)
		}
	})
}

func TestModelPricing(t *testing.T) {
	cfg := &Config{
		PricingMatrix: PricingMatrixConfig{
			DefaultInputCostPerM:  3.00,
			DefaultOutputCostPerM: 15.00,
			Models: map[string]ModelPricing{
				"claude-sonnet-4-6-20251001": {InputCostPerM: 3.00, OutputCostPerM: 15.00},
				"claude-sonnet-4-6":          {InputCostPerM: 3.00, OutputCostPerM: 15.00},
				"gpt-4o":                     {InputCostPerM: 2.50, OutputCostPerM: 10.00},
				"gpt-4o-mini":                {InputCostPerM: 0.15, OutputCostPerM: 0.60},
			},
		},
	}

	tests := []struct {
		name       string
		model      string
		wantInput  float64
		wantOutput float64
	}{
		{
			name:       "exact match",
			model:      "gpt-4o",
			wantInput:  2.50,
			wantOutput: 10.00,
		},
		{
			name:       "exact match with versioned model",
			model:      "claude-sonnet-4-6-20251001",
			wantInput:  3.00,
			wantOutput: 15.00,
		},
		{
			name:       "unknown model falls back to defaults",
			model:      "unknown-model",
			wantInput:  3.00,
			wantOutput: 15.00,
		},
		{
			name:       "prefix match picks longer key first",
			model:      "gpt-4o-mini-2024-07-18",
			wantInput:  0.15,
			wantOutput: 0.60,
		},
		{
			name:       "prefix match for versioned claude model",
			model:      "claude-sonnet-4-6-some-extra",
			wantInput:  3.00,
			wantOutput: 15.00,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := cfg.ModelPricing(tt.model)
			if got.InputCostPerM != tt.wantInput {
				t.Errorf("InputCostPerM = %v, want %v", got.InputCostPerM, tt.wantInput)
			}
			if got.OutputCostPerM != tt.wantOutput {
				t.Errorf("OutputCostPerM = %v, want %v", got.OutputCostPerM, tt.wantOutput)
			}
		})
	}
}
