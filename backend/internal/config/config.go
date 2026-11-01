package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	AppEnv  string
	AppPort string

	DBHost     string
	DBPort     int
	DBName     string
	DBUser     string
	DBPassword string
	DBSSLMode  string
}

func Load() (*Config, error) {
	// Load the root .env for local development.
	// Existing environment variables take precedence.
	if err := godotenv.Load("../.env"); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("load environment file: %w", err)
	}

	dbPort, err := strconv.Atoi(getEnv("POSTGRES_PORT", "5432"))
	if err != nil || dbPort < 1 || dbPort > 65535 {
		return nil, fmt.Errorf("invalid POSTGRES_PORT")
	}

	cfg := &Config{
		AppEnv:     getEnv("APP_ENV", "development"),
		AppPort:    getEnv("APP_PORT", "8080"),
		DBHost:     getEnv("DB_HOST", "127.0.0.1"),
		DBPort:     dbPort,
		DBName:     os.Getenv("POSTGRES_DB"),
		DBUser:     os.Getenv("POSTGRES_USER"),
		DBPassword: os.Getenv("POSTGRES_PASSWORD"),
		DBSSLMode:  getEnv("DB_SSLMODE", "disable"),
	}

	if cfg.DBName == "" || cfg.DBUser == "" || cfg.DBPassword == "" {
		return nil, fmt.Errorf("database configuration is incomplete")
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}

	return fallback
}
