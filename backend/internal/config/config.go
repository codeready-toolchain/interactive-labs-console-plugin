package config

import (
	"log/slog"
	"os"
	"strconv"
	"time"
)

var (
	DefaultCacheRefreshInterval        = 1 * time.Hour
	defaultCacheConcurrentRefreshLimit = uint(4)
)

type Config struct {
	Namespace                   string
	CacheRefreshInterval        time.Duration
	CacheConcurrentRefreshLimit uint
}

func LoadFromEnv(log *slog.Logger) Config {
	return Config{
		Namespace:                   os.Getenv("NAMESPACE"),
		CacheRefreshInterval:        parseTimeOrDefault(log, "CACHE_REFRESH_INTERVAL", DefaultCacheRefreshInterval),
		CacheConcurrentRefreshLimit: parseUintOrDefault(log, "CACHE_CONCURRENT_REFRESH_LIMIT", defaultCacheConcurrentRefreshLimit),
	}
}

func parseTimeOrDefault(log *slog.Logger, envvar string, def time.Duration) time.Duration {
	val := os.Getenv(envvar)

	if val == "" {
		return def
	}
	duration, err := time.ParseDuration(val)
	if err != nil || duration <= 0 {
		logInvalidConfiguration(log, envvar, err)
		return def
	}
	return duration
}

func parseUintOrDefault(log *slog.Logger, envvar string, def uint) uint {
	val := os.Getenv(envvar)

	if val == "" {
		return def
	}
	limit, err := strconv.ParseUint(val, 10, 64)
	if err != nil || limit == 0 {
		logInvalidConfiguration(log, envvar, err)
		return def
	}
	return uint(limit)
}

func logInvalidConfiguration(log *slog.Logger, envvar string, err error) {
	if log != nil {
		log.Error("invalid configuration", "envvar", envvar, "error", err)
	}
}
