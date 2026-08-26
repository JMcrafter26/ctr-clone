package config

import (
	"os"
	"strconv"
)

type Config struct {
	Addr         string
	DBPath       string
	LogsYAML     string
	ContactEmail string
	Token        string // if set, gates /v1/find, /v1/top, /v1/stream
	RateLimit    int    // per IP per UTC day, default 100
	EnableIngest bool   // whether to run CT followers
}

func FromEnv() Config {
	cfg := Config{
		Addr:         envOr("ADDR", ":8080"),
		DBPath:       envOr("DB_PATH", "ctindex.db"),
		LogsYAML:     envOr("LOGS_YAML", "logs.yaml"),
		ContactEmail: envOr("CONTACT_EMAIL", "admin@example.com"),
		Token:        os.Getenv("CLONE_TOKEN"),
		RateLimit:    0, // 0 = disabled (self-host default). Set RATE_LIMIT=100 to enable.
		EnableIngest: true,
	}
	if v := os.Getenv("RATE_LIMIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			// allow 0 to explicitly disable, >0 to enable
			if n >= 0 {
				cfg.RateLimit = n
			}
		}
	}
	if v := os.Getenv("ENABLE_INGEST"); v != "" {
		cfg.EnableIngest = v != "0" && v != "false"
	}
	return cfg
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
