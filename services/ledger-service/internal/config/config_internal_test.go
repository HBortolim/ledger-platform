package config

import (
	"strings"
	"testing"
	"time"
)

func setBaseEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("KAFKA_BROKERS", "localhost:9092")
	t.Setenv("RECONCILIATION_ENABLED", "")
	t.Setenv("RECONCILIATION_INTERVAL", "")
}

func TestLoad_ReconciliationDefaults(t *testing.T) {
	setBaseEnv(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() = %v", err)
	}
	if !cfg.ReconciliationEnabled || cfg.ReconciliationInterval != time.Hour {
		t.Errorf("got %v / %v, want true / 1h", cfg.ReconciliationEnabled, cfg.ReconciliationInterval)
	}
}

func TestLoad_ReconciliationRejectsBadValues(t *testing.T) {
	cases := []struct{ name, key, value string }{
		{"zero interval", "RECONCILIATION_INTERVAL", "0"},
		{"negative interval", "RECONCILIATION_INTERVAL", "-5m"},
		{"garbage interval", "RECONCILIATION_INTERVAL", "garbage"},
		{"bad enabled", "RECONCILIATION_ENABLED", "maybe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setBaseEnv(t)
			t.Setenv(tc.key, tc.value)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), tc.key) {
				t.Errorf("Load() error = %v, want one naming %s", err, tc.key)
			}
		})
	}
}
