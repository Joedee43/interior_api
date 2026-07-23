package config

import (
	"os"

	"github.com/joho/godotenv"
)

type Config struct {
	Port        string
	DatabaseURL string
}

func Load() Config {
	_ = godotenv.Load(".env", "../.env", "../../.env")

	cfg := Config{
		Port:        getFirstEnv([]string{"SERVER_PORT", "PORT"}, "8080"),
		DatabaseURL: os.Getenv("DATABASE_URL"),
	}

	return cfg
}

func getFirstEnv(keys []string, fallback string) string {
	for _, key := range keys {
		if value := os.Getenv(key); value != "" {
			return value
		}
	}
	return fallback
}
