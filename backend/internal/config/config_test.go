package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const testPassword = "s3cret-Pa55word"

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func requiredVars(extra map[string]string) map[string]string {
	values := map[string]string{
		"POSTGRES_DB":       "app_db",
		"POSTGRES_USER":     "app_user",
		"POSTGRES_PASSWORD": testPassword,
	}
	for key, value := range extra {
		values[key] = value
	}

	return values
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(lookupFrom(requiredVars(nil)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := Config{
		Env:      EnvDevelopment,
		LogLevel: slog.LevelDebug,
		HTTP: HTTP{
			Host:            "127.0.0.1",
			Port:            8080,
			ShutdownTimeout: 10 * time.Second,
		},
		Database: Database{
			Host:             "127.0.0.1",
			Port:             5432,
			Name:             "app_db",
			User:             "app_user",
			Password:         testPassword,
			SSLMode:          "disable",
			MaxConns:         10,
			MinConns:         2,
			StatementTimeout: 30 * time.Second,
		},
	}
	if *cfg != want {
		t.Errorf("parse defaults:\n got %+v\nwant %+v", *cfg, want)
	}
}

func TestParseCustomValues(t *testing.T) {
	cfg, err := parse(lookupFrom(requiredVars(map[string]string{
		"APP_ENV":              "staging",
		"APP_HOST":             "0.0.0.0",
		"APP_PORT":             "9090",
		"LOG_LEVEL":            "warn",
		"SHUTDOWN_TIMEOUT":     "25s",
		"DB_HOST":              "db.internal",
		"POSTGRES_PORT":        "6543",
		"DB_SSLMODE":           "verify-full",
		"DB_MAX_CONNS":         "20",
		"DB_MIN_CONNS":         "0",
		"DB_STATEMENT_TIMEOUT": "0",
	})))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.Env != EnvStaging || cfg.HTTP.Host != "0.0.0.0" ||
		cfg.HTTP.Port != 9090 || cfg.LogLevel != slog.LevelWarn ||
		cfg.HTTP.ShutdownTimeout != 25*time.Second {
		t.Errorf("unexpected app config: %+v", cfg)
	}
	db := cfg.Database
	if db.Host != "db.internal" || db.Port != 6543 ||
		db.SSLMode != "verify-full" || db.MaxConns != 20 ||
		db.MinConns != 0 || db.StatementTimeout != 0 {
		t.Errorf("unexpected database config: %+v", db)
	}
}

func TestParseDefaultLogLevelByEnv(t *testing.T) {
	tests := map[Env]slog.Level{
		EnvDevelopment: slog.LevelDebug,
		EnvTest:        slog.LevelInfo,
		EnvStaging:     slog.LevelInfo,
		EnvProduction:  slog.LevelInfo,
	}

	for env, want := range tests {
		t.Run(string(env), func(t *testing.T) {
			cfg, err := parse(lookupFrom(requiredVars(map[string]string{
				"APP_ENV":    string(env),
				"DB_SSLMODE": "require",
			})))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.LogLevel != want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, want)
			}
		})
	}
}

func TestParseInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"unknown env", "APP_ENV", "prod", "invalid APP_ENV"},
		{"non-numeric app port", "APP_PORT", "abc", "invalid APP_PORT"},
		{"zero app port", "APP_PORT", "0", "invalid APP_PORT"},
		{"app port too large", "APP_PORT", "70000", "invalid APP_PORT"},
		{"non-numeric db port", "POSTGRES_PORT", "x", "invalid POSTGRES_PORT"},
		{"unknown ssl mode", "DB_SSLMODE", "bogus", "invalid DB_SSLMODE"},
		{"unknown log level", "LOG_LEVEL", "loud", "invalid LOG_LEVEL"},
		{"shutdown without unit", "SHUTDOWN_TIMEOUT", "10", "invalid SHUTDOWN_TIMEOUT"},
		{"negative shutdown", "SHUTDOWN_TIMEOUT", "-1s", "invalid SHUTDOWN_TIMEOUT"},
		{"zero shutdown", "SHUTDOWN_TIMEOUT", "0s", "invalid SHUTDOWN_TIMEOUT"},
		{"zero max conns", "DB_MAX_CONNS", "0", "invalid DB_MAX_CONNS"},
		{"non-numeric max conns", "DB_MAX_CONNS", "many", "invalid DB_MAX_CONNS"},
		{"negative min conns", "DB_MIN_CONNS", "-1", "invalid DB_MIN_CONNS"},
		{"min above max", "DB_MIN_CONNS", "11", "must not exceed DB_MAX_CONNS"},
		{"negative statement timeout", "DB_STATEMENT_TIMEOUT", "-1s", "invalid DB_STATEMENT_TIMEOUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(lookupFrom(requiredVars(map[string]string{
				tt.key: tt.value,
			})))
			if err == nil {
				t.Fatalf("parse succeeded with %s=%q", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseReportsAllErrors(t *testing.T) {
	_, err := parse(lookupFrom(map[string]string{
		"APP_ENV":    "prod",
		"APP_PORT":   "abc",
		"DB_SSLMODE": "bogus",
	}))
	if err == nil {
		t.Fatal("parse succeeded with invalid configuration")
	}

	for _, want := range []string{
		"invalid APP_ENV",
		"invalid APP_PORT",
		"invalid DB_SSLMODE",
		"POSTGRES_DB is required",
		"POSTGRES_USER is required",
		"POSTGRES_PASSWORD is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestParseProductionSSLMode(t *testing.T) {
	tests := map[string]bool{
		"disable":     false,
		"allow":       false,
		"prefer":      false,
		"require":     true,
		"verify-ca":   true,
		"verify-full": true,
	}

	for mode, allowed := range tests {
		t.Run(mode, func(t *testing.T) {
			_, err := parse(lookupFrom(requiredVars(map[string]string{
				"APP_ENV":    "production",
				"DB_SSLMODE": mode,
			})))
			if allowed && err != nil {
				t.Errorf("DB_SSLMODE=%s rejected in production: %v", mode, err)
			}
			if !allowed && err == nil {
				t.Errorf("DB_SSLMODE=%s accepted in production", mode)
			}
		})
	}
}

func TestParseErrorsNeverContainPassword(t *testing.T) {
	_, err := parse(lookupFrom(requiredVars(map[string]string{
		"APP_ENV":      "prod",
		"APP_PORT":     "abc",
		"DB_SSLMODE":   "bogus",
		"DB_MAX_CONNS": "0",
	})))
	if err == nil {
		t.Fatal("parse succeeded with invalid configuration")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error exposes the database password: %q", err)
	}
}

// clearEnv unsets every variable Load reads, restoring them after the test.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"ENV_FILE", "APP_ENV", "APP_HOST", "APP_PORT", "LOG_LEVEL",
		"SHUTDOWN_TIMEOUT", "DB_HOST", "POSTGRES_PORT", "POSTGRES_DB",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "DB_SSLMODE", "DB_MAX_CONNS",
		"DB_MIN_CONNS", "DB_STATEMENT_TIMEOUT",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	return path
}

const envFileContent = `POSTGRES_DB=file_db
POSTGRES_USER=file_user
POSTGRES_PASSWORD=file_password
APP_PORT=7000
DB_HOST=file-host
`

func TestLoadPrecedence(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, envFileContent)
	t.Setenv("ENV_FILE", path)
	t.Setenv("APP_PORT", "9000")
	t.Setenv("DB_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.EnvFile != path {
		t.Errorf("EnvFile = %q, want %q", cfg.EnvFile, path)
	}
	if cfg.HTTP.Port != 9000 {
		t.Errorf("process env should win: APP_PORT = %d, want 9000", cfg.HTTP.Port)
	}
	if cfg.Database.Name != "file_db" {
		t.Errorf("file should supply unset values: POSTGRES_DB = %q", cfg.Database.Name)
	}
	if cfg.Database.Host != "file-host" {
		t.Errorf("empty process value should fall through to file: DB_HOST = %q",
			cfg.Database.Host)
	}
	if cfg.HTTP.Host != "127.0.0.1" {
		t.Errorf("default should apply when unset everywhere: APP_HOST = %q",
			cfg.HTTP.Host)
	}
	if got := os.Getenv("POSTGRES_DB"); got != "" {
		t.Errorf("Load modified the process environment: POSTGRES_DB = %q", got)
	}
}

func TestLoadMissingDefaultEnvFileIsIgnored(t *testing.T) {
	clearEnv(t)
	// The default ../.env does not exist relative to this package directory.
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnvFile != "" {
		t.Errorf("EnvFile = %q, want empty", cfg.EnvFile)
	}
}

func TestLoadMissingExplicitEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with a missing explicit ENV_FILE")
	}
}

func TestLoadProductionSkipsEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnvFile != "" {
		t.Errorf("EnvFile = %q, want empty in production", cfg.EnvFile)
	}
}

const productionEnvFileContent = `APP_ENV=production
DB_SSLMODE=require
POSTGRES_DB=file_db
POSTGRES_USER=file_user
POSTGRES_PASSWORD=file-only-s3cret
`

// chdirBelowEnvFile writes content to <tmp>/.env and changes into
// <tmp>/backend, so the default ../.env path resolves to that file.
func chdirBelowEnvFile(t *testing.T, content string) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	workDir := filepath.Join(root, "backend")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	t.Chdir(workDir)

	return path
}

func TestLoadRejectsProductionFromEnvFile(t *testing.T) {
	tests := map[string]func(t *testing.T){
		"explicit ENV_FILE": func(t *testing.T) {
			t.Setenv("ENV_FILE", writeEnvFile(t, productionEnvFileContent))
		},
		"default ../.env": func(t *testing.T) {
			chdirBelowEnvFile(t, productionEnvFileContent)
		},
	}

	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			setup(t)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load accepted APP_ENV=production from an env file (Env=%q)", cfg.Env)
			}
			if cfg != nil {
				t.Errorf("Load returned a config alongside the error")
			}
			for _, want := range []string{"APP_ENV=production", "process environment"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "file-only-s3cret") {
				t.Errorf("error exposes the database password: %q", err)
			}
		})
	}
}

func TestLoadProcessEnvOverridesProductionInEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV_FILE", writeEnvFile(t, productionEnvFileContent))
	t.Setenv("APP_ENV", "staging")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvStaging {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvStaging)
	}
}

func TestLoadProductionFromProcessIgnoresEnvFile(t *testing.T) {
	clearEnv(t)
	chdirBelowEnvFile(t, productionEnvFileContent)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_SSLMODE", "verify-full")
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvProduction || cfg.EnvFile != "" {
		t.Errorf("Env = %q, EnvFile = %q; want production and no env file",
			cfg.Env, cfg.EnvFile)
	}
	db := cfg.Database
	if db.Name != "app_db" || db.User != "app_user" ||
		db.Password != testPassword || db.SSLMode != "verify-full" {
		t.Errorf("database config not taken from the process environment: "+
			"name=%q user=%q sslmode=%q", db.Name, db.User, db.SSLMode)
	}
}

func TestLoadProductionRejectsEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENV_FILE", writeEnvFile(t, envFileContent))

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ENV_FILE") {
		t.Fatalf("Load error = %v, want ENV_FILE rejection", err)
	}
}
