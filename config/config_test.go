package config

import (
	"os"
	"testing"
)

func TestLoad_MissingSessionSecret(t *testing.T) {
	os.Unsetenv("SESSION_SECRET")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when SESSION_SECRET is missing")
	}
}

func TestLoad_ShortSessionSecret(t *testing.T) {
	os.Setenv("SESSION_SECRET", "tooshort")
	defer os.Unsetenv("SESSION_SECRET")
	_, err := Load()
	if err == nil {
		t.Fatal("expected error when SESSION_SECRET is under 32 chars")
	}
}

func TestLoad_DefaultsApplied(t *testing.T) {
	os.Setenv("SESSION_SECRET", "a-very-long-secret-value-1234567890")
	defer os.Unsetenv("SESSION_SECRET")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Port != "8080" {
		t.Errorf("expected default port 8080, got %s", cfg.Port)
	}
	if cfg.DBSSLMode != "disable" {
		t.Errorf("expected default sslmode disable, got %s", cfg.DBSSLMode)
	}
}
