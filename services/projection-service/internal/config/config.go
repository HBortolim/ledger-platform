package config

import (
	"fmt"
	"os"
	"strings"
)

type Config struct {
	DatabaseURL  string
	KafkaBrokers []string
	KafkaGroupID string
	AppPort      string
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

	return &Config{
		DatabaseURL:  dbURL,
		KafkaBrokers: strings.Split(brokers, ","),
		KafkaGroupID: groupID,
		AppPort:      ":" + port,
	}, nil

}
