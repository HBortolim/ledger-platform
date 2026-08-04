package config

import (
	"fmt"
	"os"
	"strings"

	"github.com/shopspring/decimal"
)

type Config struct {
	DatabaseURL      string
	KafkaBrokers     []string
	AppPort          string
	DailyTransferCap decimal.Decimal
}

func Load() (*Config, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		return nil, fmt.Errorf("DATABASE_URL is required")
	}

	brokers := os.Getenv("KAFKA_BROKERS")
	if brokers == "" {
		return nil, fmt.Errorf("KAFKA_BROKERS is required")
	}

	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}

	capStr := os.Getenv("DAILY_TRANSFER_CAP")
	if capStr == "" {
		capStr = "100000.00"
	}
	dailyCap, err := decimal.NewFromString(capStr)
	if err != nil {
		return nil, fmt.Errorf("invalid DAILY_TRANSFER_CAP %q: %w", capStr, err)
	}

	return &Config{
		DatabaseURL:      dbURL,
		KafkaBrokers:     strings.Split(brokers, ","),
		AppPort:          ":" + port,
		DailyTransferCap: dailyCap,
	}, nil
}
