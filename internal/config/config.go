// Package config loads runtime configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Env         string // "development" or "production"
	Addr        string
	DatabaseURL string
	TrustProxy  bool // honour X-Forwarded-For / X-Real-IP (only behind a reverse proxy)

	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	// LocationSecret keys the deterministic APPROX jitter. Changing it moves every approximate pin.
	LocationSecret []byte
	// InviteCode, when set, is required to create a new account (email or Google).
	InviteCode string

	GoogleClientIDs []string // accepted "aud" values: web, iOS and Android client IDs

	Storage   StorageConfig
	SignedURL time.Duration

	SMTP SMTPConfig

	MaxUploadBytes int64
	ImageWorkers   int // concurrent image decodes; each can take ~150 MB for a large photo
}

// StorageConfig targets any S3-compatible API. In production this is Backblaze B2
// (endpoint https://s3.<region>.backblazeb2.com); MinIO works for local development.
type StorageConfig struct {
	Endpoint     string
	Region       string
	Bucket       string
	KeyID        string
	AppKey       string
	UsePathStyle bool
}

type SMTPConfig struct {
	Host     string
	Port     int
	Username string
	Password string
	From     string
}

func Load() (*Config, error) {
	c := &Config{
		Env:             env("APP_ENV", "development"),
		Addr:            env("ADDR", ":"+env("PORT", "8080")), // some hosts provide PORT
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		TrustProxy:      envBool("TRUST_PROXY", false),
		JWTSecret:       []byte(os.Getenv("JWT_SECRET")),
		AccessTokenTTL:  envDuration("ACCESS_TOKEN_TTL", 15*time.Minute),
		RefreshTokenTTL: envDuration("REFRESH_TOKEN_TTL", 60*24*time.Hour),
		LocationSecret:  []byte(os.Getenv("LOCATION_SECRET")),
		InviteCode:      os.Getenv("INVITE_CODE"),
		GoogleClientIDs: envList("GOOGLE_CLIENT_IDS"),
		Storage: StorageConfig{
			Endpoint:     os.Getenv("B2_ENDPOINT"),
			Region:       env("B2_REGION", "us-west-004"),
			Bucket:       os.Getenv("B2_BUCKET"),
			KeyID:        os.Getenv("B2_KEY_ID"),
			AppKey:       os.Getenv("B2_APP_KEY"),
			UsePathStyle: envBool("B2_PATH_STYLE", true),
		},
		SignedURL: envDuration("SIGNED_URL_TTL", 15*time.Minute),
		SMTP: SMTPConfig{
			Host:     os.Getenv("SMTP_HOST"),
			Port:     envInt("SMTP_PORT", 587),
			Username: os.Getenv("SMTP_USERNAME"),
			Password: os.Getenv("SMTP_PASSWORD"),
			From:     env("SMTP_FROM", "Meowshot <no-reply@meowshot.app>"),
		},
		MaxUploadBytes: int64(envInt("MAX_UPLOAD_MB", 15)) << 20,
		ImageWorkers:   max(1, envInt("IMAGE_WORKERS", 2)),
	}

	var errs []error
	if c.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if len(c.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET must be at least 32 bytes"))
	}
	if len(c.LocationSecret) < 32 {
		errs = append(errs, errors.New("LOCATION_SECRET must be at least 32 bytes"))
	}
	if c.Storage.Endpoint == "" || c.Storage.Bucket == "" || c.Storage.KeyID == "" || c.Storage.AppKey == "" {
		errs = append(errs, errors.New("B2_ENDPOINT, B2_BUCKET, B2_KEY_ID and B2_APP_KEY are required"))
	}
	return c, errors.Join(errs...)
}

func (c *Config) IsProduction() bool { return c.Env == "production" }

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envBool(key string, def bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func envInt(key string, def int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil {
		return def
	}
	return v
}

func envDuration(key string, def time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	d, err := time.ParseDuration(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid duration for %s: %v", key, err))
	}
	return d
}

func envList(key string) []string {
	var out []string
	for _, p := range strings.Split(os.Getenv(key), ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
