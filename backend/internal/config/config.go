package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

const defaultEnvFile = "../.env"

var validEnvs = []Env{EnvDevelopment, EnvTest, EnvStaging, EnvProduction}

var validSSLModes = []string{
	"disable", "allow", "prefer", "require", "verify-ca", "verify-full",
}

// insecureSSLModes are rejected in production because they permit
// unencrypted connections.
var insecureSSLModes = []string{"disable", "allow", "prefer"}

type Config struct {
	Env Env
	// EnvFile is the path of the environment file that was read, or ""
	// when none was read.
	EnvFile  string
	LogLevel slog.Level

	HTTP     HTTP
	Database Database
}

type HTTP struct {
	Host            string
	Port            int
	ShutdownTimeout time.Duration
}

type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string

	MaxConns         int32
	MinConns         int32
	StatementTimeout time.Duration
}

// Load reads configuration from the process environment and, outside
// production, from an environment file (ENV_FILE, default ../.env
// relative to the working directory). Non-empty process variables take
// precedence over file values. The process environment is not modified.
func Load() (*Config, error) {
	envFile, fileValues, err := readEnvFile()
	if err != nil {
		return nil, err
	}

	lookup := func(key string) (string, bool) {
		if value := os.Getenv(key); value != "" {
			return value, true
		}
		if value := fileValues[key]; value != "" {
			return value, true
		}
		return "", false
	}

	// Production must be selected by the process environment. readEnvFile
	// never reads a file when it is, so production here means the file
	// supplied APP_ENV.
	if value, _ := lookup("APP_ENV"); Env(value) == EnvProduction && envFile != "" {
		return nil, fmt.Errorf(
			"APP_ENV=production was read from environment file %s: "+
				"set APP_ENV in the process environment; "+
				"environment files are not read in production",
			envFile,
		)
	}

	cfg, err := parse(lookup)
	if err != nil {
		return nil, err
	}
	cfg.EnvFile = envFile

	return cfg, nil
}

func readEnvFile() (string, map[string]string, error) {
	path, explicit := os.LookupEnv("ENV_FILE")

	if Env(os.Getenv("APP_ENV")) == EnvProduction {
		if explicit {
			return "", nil, errors.New(
				"ENV_FILE must not be set when APP_ENV=production",
			)
		}
		return "", nil, nil
	}

	if !explicit || path == "" {
		path = defaultEnvFile
		explicit = false
	}

	values, err := godotenv.Read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("read environment file %s: %w", path, err)
	}

	return path, values, nil
}

// parse builds and validates a Config from lookup, reporting every
// invalid value at once. It never includes POSTGRES_PASSWORD in errors.
func parse(lookup func(string) (string, bool)) (*Config, error) {
	var errs []error

	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok && value != "" {
			return value
		}
		return fallback
	}

	cfg := &Config{
		Env: Env(get("APP_ENV", string(EnvDevelopment))),
		HTTP: HTTP{
			Host: get("APP_HOST", "127.0.0.1"),
		},
		Database: Database{
			Host:     get("DB_HOST", "127.0.0.1"),
			Name:     get("POSTGRES_DB", ""),
			User:     get("POSTGRES_USER", ""),
			Password: get("POSTGRES_PASSWORD", ""),
			SSLMode:  get("DB_SSLMODE", "disable"),
		},
	}

	if !slices.Contains(validEnvs, cfg.Env) {
		errs = append(errs, fmt.Errorf(
			"invalid APP_ENV %q: must be one of %v", cfg.Env, validEnvs,
		))
	}

	defaultLogLevel := "info"
	if cfg.Env == EnvDevelopment {
		defaultLogLevel = "debug"
	}
	logLevel := get("LOG_LEVEL", defaultLogLevel)
	if err := cfg.LogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		errs = append(errs, fmt.Errorf(
			"invalid LOG_LEVEL %q: must be debug, info, warn or error", logLevel,
		))
	}

	var err error
	if cfg.HTTP.Port, err = parsePort("APP_PORT", get("APP_PORT", "8080")); err != nil {
		errs = append(errs, err)
	}
	if cfg.HTTP.ShutdownTimeout, err = parseDuration(
		"SHUTDOWN_TIMEOUT", get("SHUTDOWN_TIMEOUT", "10s"), false,
	); err != nil {
		errs = append(errs, err)
	}

	if cfg.Database.Port, err = parsePort(
		"POSTGRES_PORT", get("POSTGRES_PORT", "5432"),
	); err != nil {
		errs = append(errs, err)
	}

	for _, required := range []struct{ key, value string }{
		{"POSTGRES_DB", cfg.Database.Name},
		{"POSTGRES_USER", cfg.Database.User},
		{"POSTGRES_PASSWORD", cfg.Database.Password},
	} {
		if required.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", required.key))
		}
	}

	if !slices.Contains(validSSLModes, cfg.Database.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"invalid DB_SSLMODE %q: must be one of %v",
			cfg.Database.SSLMode, validSSLModes,
		))
	} else if cfg.Env == EnvProduction &&
		slices.Contains(insecureSSLModes, cfg.Database.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"DB_SSLMODE %q is not allowed in production: "+
				"use require, verify-ca or verify-full",
			cfg.Database.SSLMode,
		))
	}

	maxConns, maxErr := parseInt32("DB_MAX_CONNS", get("DB_MAX_CONNS", "10"), 1)
	if maxErr != nil {
		errs = append(errs, maxErr)
	}
	minConns, minErr := parseInt32("DB_MIN_CONNS", get("DB_MIN_CONNS", "2"), 0)
	if minErr != nil {
		errs = append(errs, minErr)
	}
	if maxErr == nil && minErr == nil && minConns > maxConns {
		errs = append(errs, fmt.Errorf(
			"DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)",
			minConns, maxConns,
		))
	}
	cfg.Database.MaxConns = maxConns
	cfg.Database.MinConns = minConns

	if cfg.Database.StatementTimeout, err = parseDuration(
		"DB_STATEMENT_TIMEOUT", get("DB_STATEMENT_TIMEOUT", "30s"), true,
	); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}

	return cfg, nil
}

func parsePort(key, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf(
			"invalid %s %q: must be an integer between 1 and 65535", key, value,
		)
	}

	return port, nil
}

func parseDuration(key, value string, allowZero bool) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		bound := "positive"
		if allowZero {
			bound = "non-negative"
		}
		return 0, fmt.Errorf(
			"invalid %s %q: must be a %s duration such as 10s", key, value, bound,
		)
	}

	return d, nil
}

func parseInt32(key, value string, minimum int32) (int32, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || int32(n) < minimum {
		return 0, fmt.Errorf(
			"invalid %s %q: must be an integer >= %d", key, value, minimum,
		)
	}

	return int32(n), nil
}
