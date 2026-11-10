package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL  string
	KafkaBrokers []string
	KafkaGroupID string
	AppPort      string

	// PendingEntryAlertAfter is how long an entry may sit in pending_entries before the consumer
	// logs an error naming the wallet and the missing seq (Task 08, SEQ-9).
	PendingEntryAlertAfter time.Duration
}

const (
	defaultGroupID = "projection-service"
	defaultPort    = "8082"
)

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS is required")
	}

	groupID := os.Getenv("KAFKA_GROUP_ID")
	if groupID == "" {
		groupID = defaultGroupID
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = defaultPort
	}

	alertAfterStr := os.Getenv("PENDING_ENTRY_ALERT_AFTER")
	if alertAfterStr == "" {
		alertAfterStr = "30s"
	}
	alertAfter, err := time.ParseDuration(alertAfterStr)
	if err != nil {
		return nil, fmt.Errorf("invalid PENDING_ENTRY_ALERT_AFTER %q: %w", alertAfterStr, err)
	}
	if alertAfter <= 0 {
		return nil, fmt.Errorf("invalid PENDING_ENTRY_ALERT_AFTER %q: must be positive", alertAfterStr)
	}

	return &Config{
		DatabaseURL:            dbURL,
		KafkaBrokers:           strings.Split(brokers, ","),
		KafkaGroupID:           groupID,
		AppPort:                ":" + port,
		PendingEntryAlertAfter: alertAfter,
	}, nil

}
