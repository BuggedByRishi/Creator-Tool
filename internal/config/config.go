package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	Env           string // "dev" or "prod"
	HTTPAddr      string
	DatabaseURL   string
	SessionTTL    time.Duration
	CookieSecure  bool
	AutoMigrate   bool
	AllowedOrigin string // web app origin for CORS, e.g. http://localhost:3000
}

func Load() (Config, error) {
	c := Config{
		Env:           env("APP_ENV", "dev"),
		HTTPAddr:      env("HTTP_ADDR", ":8080"),
		DatabaseURL:   os.Getenv("DATABASE_URL"),
		SessionTTL:    time.Duration(envInt("SESSION_TTL_HOURS", 24*30)) * time.Hour,
		AutoMigrate:   envBool("AUTO_MIGRATE", true),
		AllowedOrigin: env("ALLOWED_ORIGIN", "http://localhost:3000"),
	}
	c.CookieSecure = envBool("COOKIE_SECURE", c.Env == "prod")
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envInt(k string, def int) int {
	if v, err := strconv.Atoi(os.Getenv(k)); err == nil {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	if v, err := strconv.ParseBool(os.Getenv(k)); err == nil {
		return v
	}
	return def
}
