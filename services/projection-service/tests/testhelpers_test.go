package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/ory/dockertest/v3"
	dc "github.com/ory/dockertest/v3/docker"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"
)

// setupProjectionDB spins an ephemeral Postgres container via dockertest,
// applies services/projection-service/migrations/ against it as the owner
// role (`ledger`), and returns both the owner DSN (migrations, schema
// assertions) and the app DSN (the restricted `projection_app` role the
// service actually runs as).
func setupProjectionDB(t *testing.T) (ownerDSN, appDSN string) {
	t.Helper()

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	resource, err := pool.Run("postgres", "16-alpine", []string{
		"POSTGRES_USER=ledger",
		"POSTGRES_PASSWORD=ledger",
		"POSTGRES_DB=ledger",
	})
	if err != nil {
		t.Fatalf("could not start postgres container: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Purge(resource); err != nil {
			t.Logf("could not purge postgres container: %v", err)
		}
	})

	hostPort := resource.GetPort("5432/tcp")
	ownerDSN = fmt.Sprintf("postgres://ledger:ledger@localhost:%s/ledger?sslmode=disable", hostPort)
	appDSN = fmt.Sprintf("postgres://projection_app:projection_app@localhost:%s/ledger?sslmode=disable", hostPort)

	ctx := context.Background()
	if err := pool.Retry(func() error {
		conn, err := pgx.Connect(ctx, ownerDSN)
		if err != nil {
			return err
		}
		defer func() { _ = conn.Close(ctx) }()
		return conn.Ping(ctx)
	}); err != nil {
		t.Fatalf("postgres never became ready: %v", err)
	}

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=projection_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrations failed to apply: %v", err)
	}

	return ownerDSN, appDSN
}

// setupKafka spins an ephemeral single-node KRaft Kafka broker via dockertest
// and returns its host-reachable bootstrap address, with ledger.posted.v1
// pre-created. KRaft's advertised listener must be known at container boot,
// so this binds a fixed host port rather than using dockertest's dynamic
// port assignment.
func setupKafka(t *testing.T) (bootstrap string) {
	t.Helper()

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	const hostPort = "19093"
	bootstrap = "localhost:" + hostPort

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "confluentinc/cp-kafka",
		Tag:        "7.6.0",
		Env: []string{
			"KAFKA_NODE_ID=1",
			"KAFKA_PROCESS_ROLES=broker,controller",
			"KAFKA_LISTENER_SECURITY_PROTOCOL_MAP=CONTROLLER:PLAINTEXT,PLAINTEXT:PLAINTEXT",
			"KAFKA_LISTENERS=PLAINTEXT://0.0.0.0:9092,CONTROLLER://0.0.0.0:9093",
			"KAFKA_ADVERTISED_LISTENERS=PLAINTEXT://" + bootstrap,
			"KAFKA_CONTROLLER_LISTENER_NAMES=CONTROLLER",
			"KAFKA_INTER_BROKER_LISTENER_NAME=PLAINTEXT",
			"KAFKA_CONTROLLER_QUORUM_VOTERS=1@localhost:9093",
			"KAFKA_OFFSETS_TOPIC_REPLICATION_FACTOR=1",
			"KAFKA_TRANSACTION_STATE_LOG_REPLICATION_FACTOR=1",
			"KAFKA_TRANSACTION_STATE_LOG_MIN_ISR=1",
			"KAFKA_AUTO_CREATE_TOPICS_ENABLE=true",
			"CLUSTER_ID=MkU3OEVBNTcwNTJENDM2Qk",
		},
		ExposedPorts: []string{"9092/tcp"},
		PortBindings: map[dc.Port][]dc.PortBinding{
			"9092/tcp": {{HostIP: "0.0.0.0", HostPort: hostPort}},
		},
	})
	if err != nil {
		t.Fatalf("could not start kafka container: %v", err)
	}
	t.Cleanup(func() {
		if err := pool.Purge(resource); err != nil {
			t.Logf("could not purge kafka container: %v", err)
		}
	})

	pool.MaxWait = 60 * time.Second
	if err := pool.Retry(func() error {
		cl, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
		if err != nil {
			return err
		}
		defer cl.Close()
		pingCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return cl.Ping(pingCtx)
	}); err != nil {
		t.Fatalf("kafka never became ready: %v", err)
	}

	adminCl, err := kgo.NewClient(kgo.SeedBrokers(bootstrap))
	if err != nil {
		t.Fatalf("kgo.NewClient() (admin) = error %v, want nil", err)
	}
	defer adminCl.Close()
	admin := kadm.NewClient(adminCl)
	createCtx, createCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer createCancel()
	if _, err := admin.CreateTopics(createCtx, 1, 1, nil, "ledger.posted.v1"); err != nil {
		t.Fatalf("could not create test topic: %v", err)
	}

	return bootstrap
}

// connectPool opens a pgxpool.Pool against dsn, closed automatically at test
// cleanup.
func connectPool(t *testing.T, dsn string) *pgxpool.Pool {
	t.Helper()
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgxpool.New() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// entryFixture is one entry in a produceLedgerPosted payload.
type entryFixture struct {
	EntryID   uuid.UUID
	AccountID uuid.UUID
	EntryType string // DEBIT | CREDIT
	Amount    string // 2dp string, e.g. "100.00"
}

// produceLedgerPosted publishes a LEDGER_POSTED message to bootstrap using
// the exact §7.2 envelope the Ledger Service's outbox produces (see
// services/ledger-service/internal/repository/posting.go, ledgerPostedPayload).
// Producing directly, rather than routing through a real ledger-service
// instance, keeps this suite independent of ledger internals.
func produceLedgerPosted(t *testing.T, bootstrap string, transactionID uuid.UUID, occurredAt time.Time, entries []entryFixture, traceparent string) {
	t.Helper()

	type entryPayload struct {
		EntryID   uuid.UUID `json:"entryId"`
		AccountID uuid.UUID `json:"accountId"`
		EntryType string    `json:"entryType"`
		Amount    string    `json:"amount"`
	}
	type payload struct {
		SchemaVersion   int            `json:"schemaVersion"`
		EventID         uuid.UUID      `json:"eventId"`
		EventType       string         `json:"eventType"`
		OccurredAt      time.Time      `json:"occurredAt"`
		TransactionID   uuid.UUID      `json:"transactionId"`
		TransactionType string         `json:"transactionType"`
		Entries         []entryPayload `json:"entries"`
		Traceparent     string         `json:"traceparent"`
	}

	eps := make([]entryPayload, len(entries))
	for i, e := range entries {
		eps[i] = entryPayload(e)
	}
	body, err := json.Marshal(payload{
		SchemaVersion:   1,
		EventID:         uuid.New(),
		EventType:       "LEDGER_POSTED",
		OccurredAt:      occurredAt,
		TransactionID:   transactionID,
		TransactionType: "TRANSFER",
		Entries:         eps,
		Traceparent:     traceparent,
	})
	if err != nil {
		t.Fatalf("marshal ledger posted payload: %v", err)
	}

	producer, err := kgo.NewClient(kgo.SeedBrokers(bootstrap), kgo.ClientID("test-producer"))
	if err != nil {
		t.Fatalf("kgo.NewClient() (producer) = error %v, want nil", err)
	}
	defer producer.Close()

	var hdrs []kgo.RecordHeader
	if traceparent != "" {
		hdrs = []kgo.RecordHeader{{Key: "traceparent", Value: []byte(traceparent)}}
	}

	produceCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res := producer.ProduceSync(produceCtx, &kgo.Record{
		Topic:   "ledger.posted.v1",
		Key:     []byte(transactionID.String()),
		Value:   body,
		Headers: hdrs,
	})
	if err := res.FirstErr(); err != nil {
		t.Fatalf("produce ledger posted message: %v", err)
	}
}
