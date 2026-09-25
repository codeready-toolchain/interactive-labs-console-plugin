package config

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLoadFromEnvReadsValues(t *testing.T) {
	t.Setenv("NAMESPACE", "labs")
	t.Setenv("CACHE_REFRESH_INTERVAL", "15m")
	t.Setenv("CACHE_CONCURRENT_REFRESH_LIMIT", "8")
	var logs bytes.Buffer

	cfg := LoadFromEnv(slog.New(slog.NewTextHandler(&logs, nil)))

	require.Equal(t, Config{
		Namespace:                   "labs",
		CacheRefreshInterval:        15 * time.Minute,
		CacheConcurrentRefreshLimit: 8,
	}, cfg)
	require.Empty(t, logs.String())
}

func TestLoadFromEnvDefaultsAndLogsInvalidCacheRefreshValues(t *testing.T) {
	for _, tc := range []struct {
		name, variable, value string
	}{
		{"malformed interval", "CACHE_REFRESH_INTERVAL", "invalid-interval"},
		{"zero interval", "CACHE_REFRESH_INTERVAL", "0s"},
		{"malformed concurrency", "CACHE_CONCURRENT_REFRESH_LIMIT", "invalid-concurrency"},
		{"zero concurrency", "CACHE_CONCURRENT_REFRESH_LIMIT", "0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("NAMESPACE", "")
			t.Setenv("CACHE_REFRESH_INTERVAL", "")
			t.Setenv("CACHE_CONCURRENT_REFRESH_LIMIT", "")
			t.Setenv(tc.variable, tc.value)
			var logs bytes.Buffer

			cfg := LoadFromEnv(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
				if attr.Key == slog.TimeKey {
					return slog.Attr{}
				}
				return attr
			}})))

			require.Equal(t, Config{CacheRefreshInterval: DefaultCacheRefreshInterval, CacheConcurrentRefreshLimit: defaultCacheConcurrentRefreshLimit}, cfg)
			require.Contains(t, logs.String(), `msg="invalid configuration"`)
			require.Contains(t, logs.String(), "envvar="+tc.variable)
			require.Contains(t, logs.String(), "error=")
		})
	}
}
