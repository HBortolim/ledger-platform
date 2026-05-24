#!/bin/sh
# Creates required Kafka topics. Run once after the broker is healthy.
BROKER="${KAFKA_BROKERS:-localhost:9092}"

kafka-topics.sh --bootstrap-server "$BROKER" --create --if-not-exists \
  --topic ledger.posted.v1 --partitions 12 --replication-factor 1

kafka-topics.sh --bootstrap-server "$BROKER" --create --if-not-exists \
  --topic projection.balance.updated.v1 --partitions 12 --replication-factor 1

echo "Topics created."
