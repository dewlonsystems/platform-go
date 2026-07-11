// Package config loads application configuration from environment
// variables (optionally via a .env file in development).
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dewlonsystems/platform-go/errors"
	"github.com/dewlonsystems/platform-go/logger"
)

// Config holds all runtime configuration for the service.
type Config struct {
	Env  string // "development", "staging", "production"
	Port string

	// Database
	DBHost     string
	DBPort     string
	DBUser     string
	DBPassword string
	DBName     string
	DBSSLMode  string // "disable" locally, "require" in production

	// Auth / sessions
	SessionSecret string // pepper used to HMAC session tokens before storing
	SessionTTL    time.Duration

	// SMTP
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string

	// Rate limiting
	RateLimitRequests int
	RateLimitWindow   time.Duration
}

// Load reads configuration from the process environment. If a .env file
// exists in the current directory, it's loaded first (without overriding
// any variable already set in the real environment) — handy for local dev,
// harmless in production where a real environment is used instead.
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, err
	}

	cfg := &Config{
		Env:  getEnvDefault("APP_ENV", "development"),
		Port: getEnvDefault("PORT", "8080"),

		DBHost:     getEnvDefault("DB_HOST", "localhost"),
		DBPort:     getEnvDefault("DB_PORT", "5432"),
		DBUser:     getEnvDefault("DB_USER", "postgres"),
		DBPassword: os.Getenv("DB_PASSWORD"),
		DBName:     getEnvDefault("DB_NAME", "app"),
		DBSSLMode:  getEnvDefault("DB_SSLMODE", "disable"),

		SMTPHost:     os.Getenv("SMTP_HOST"),
		SMTPPort:     getEnvDefault("SMTP_PORT", "587"),
		SMTPUsername: os.Getenv("SMTP_USERNAME"),
		SMTPPassword: os.Getenv("SMTP_PASSWORD"),
		SMTPFrom:     getEnvDefault("SMTP_FROM", "no-reply@example.com"),
	}

	var err error
	if cfg.SessionSecret, err = mustGetEnv("SESSION_SECRET"); err != nil {
		return nil, err
	}
	if len(cfg.SessionSecret) < 32 {
		return nil, errors.NewBadInput("SESSION_SECRET must be at least 32 characters", nil)
	}

	if cfg.SessionTTL, err = getEnvDurationDefault("SESSION_TTL", 30*24*time.Hour); err != nil {
		return nil, err
	}
	if cfg.RateLimitRequests, err = getEnvIntDefault("RATE_LIMIT_REQUESTS", 100); err != nil {
		return nil, err
	}
	if cfg.RateLimitWindow, err = getEnvDurationDefault("RATE_LIMIT_WINDOW", time.Minute); err != nil {
		return nil, err
	}

	if cfg.Env == "production" && cfg.DBSSLMode == "disable" {
		logger.Log.Warn("DB_SSLMODE=disable in production; connections to the database will be unencrypted")
	}

	return cfg, nil
}

// DSN builds a Postgres connection string suitable for database/sql + lib/pq.
func (c *Config) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=%s",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName, c.DBSSLMode,
	)
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

// -----------------------------------------------------------------------------
// helpers
// -----------------------------------------------------------------------------

func mustGetEnv(key string) (string, error) {
	v := os.Getenv(key)
	if v == "" {
		return "", errors.NewBadInput(fmt.Sprintf("missing required environment variable %s", key), nil)
	}
	return v, nil
}

func getEnvDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvIntDefault(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, errors.NewBadInput(fmt.Sprintf("environment variable %s must be an integer", key), err)
	}
	return n, nil
}

func getEnvDurationDefault(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, errors.NewBadInput(fmt.Sprintf("environment variable %s must be a duration (e.g. 30m, 24h)", key), err)
	}
	return d, nil
}

// loadDotEnv parses a simple KEY=VALUE .env file, one entry per line,
// skipping blank lines and lines starting with '#'. It never overrides a
// variable that's already set in the real environment. If the file doesn't
// exist, it silently does nothing — a .env file is a dev convenience, not
// a requirement. If the file exists but fails to read partway through
// (e.g. a line exceeds the scanner's buffer, or an I/O error occurs), it
// returns an error so the caller can decide how to handle it.
func loadDotEnv(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return nil // missing .env is fine, nothing more to do
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.Trim(strings.TrimSpace(value), `"'`)
		if _, exists := os.LookupEnv(key); !exists {
			os.Setenv(key, value)
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("reading %s: %w", path, err)
	}
	return nil
}
