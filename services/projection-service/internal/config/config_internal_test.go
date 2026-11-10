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
	t.Setenv("PENDING_ENTRY_ALERT_AFTER", "")
}

func TestLoad_PendingEntryAlertAfter(t *testing.T) {
	setBaseEnv(t)
	cfg, err := Load()
	if err != nil || cfg.PendingEntryAlertAfter != 30*time.Second {
		t.Fatalf("default = %v, err=%v, want 30s", cfg.PendingEntryAlertAfter, err)
	}

	t.Setenv("PENDING_ENTRY_ALERT_AFTER", "2m")
	if cfg, err := Load(); err != nil || cfg.PendingEntryAlertAfter != 2*time.Minute {
		t.Errorf("2m = %v, err=%v", cfg.PendingEntryAlertAfter, err)
	}

	for _, bad := range []string{"0", "-5s", "garbage"} {
		t.Setenv("PENDING_ENTRY_ALERT_AFTER", bad)
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "PENDING_ENTRY_ALERT_AFTER") {
			t.Errorf("%q: Load() error = %v, want one naming PENDING_ENTRY_ALERT_AFTER", bad, err)
		}
	}
}
