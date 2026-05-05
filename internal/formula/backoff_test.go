package formula

import "testing"

func TestParseBackoffConfig_ValidFull(t *testing.T) {
	cfg := ParseBackoffConfig("base=30s, multiplier=3, max=10m")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Base != "30s" {
		t.Errorf("Base = %q, want %q", cfg.Base, "30s")
	}
	if cfg.Multiplier != 3 {
		t.Errorf("Multiplier = %d, want %d", cfg.Multiplier, 3)
	}
	if cfg.Max != "10m" {
		t.Errorf("Max = %q, want %q", cfg.Max, "10m")
	}
}

func TestParseBackoffConfig_DefaultMultiplier(t *testing.T) {
	cfg := ParseBackoffConfig("base=1m, max=5m")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Multiplier != 2 {
		t.Errorf("Multiplier = %d, want default %d", cfg.Multiplier, 2)
	}
}

func TestParseBackoffConfig_MissingBaseReturnsNil(t *testing.T) {
	cfg := ParseBackoffConfig("multiplier=2, max=5m")
	if cfg != nil {
		t.Errorf("expected nil for missing base, got %+v", cfg)
	}
}

func TestParseBackoffConfig_EmptyStringReturnsNil(t *testing.T) {
	cfg := ParseBackoffConfig("")
	if cfg != nil {
		t.Errorf("expected nil for empty string, got %+v", cfg)
	}
}

func TestParseBackoffConfig_BaseOnly(t *testing.T) {
	cfg := ParseBackoffConfig("base=5s")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Base != "5s" {
		t.Errorf("Base = %q, want %q", cfg.Base, "5s")
	}
	if cfg.Multiplier != 2 {
		t.Errorf("Multiplier = %d, want default %d", cfg.Multiplier, 2)
	}
	if cfg.Max != "" {
		t.Errorf("Max = %q, want empty", cfg.Max)
	}
}

func TestParseBackoffConfig_ExtraWhitespace(t *testing.T) {
	cfg := ParseBackoffConfig("  base = 30s ,  max = 10m  ")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Base != "30s" {
		t.Errorf("Base = %q, want %q", cfg.Base, "30s")
	}
	if cfg.Max != "10m" {
		t.Errorf("Max = %q, want %q", cfg.Max, "10m")
	}
}

func TestParseBackoffConfig_InvalidMultiplierIgnored(t *testing.T) {
	cfg := ParseBackoffConfig("base=1s, multiplier=abc")
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}
	if cfg.Multiplier != 2 {
		t.Errorf("Multiplier = %d, want default %d (invalid value ignored)", cfg.Multiplier, 2)
	}
}
