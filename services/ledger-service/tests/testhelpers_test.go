package tests

import (
	"context"
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
	"github.com/shopspring/decimal"
	"github.com/twmb/franz-go/pkg/kadm"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/ledger-platform/ledger-service/internal/domain"
)


var systemFundingAccountID = uuid.MustParse("00000000-0000-0000-0000-000000000001")

// testDailyCap is the shared, generously-high daily transfer cap used by tests that don't
// specifically exercise the cap itself — those construct their own PostingRepository instance
// with a tighter cap (see posting_test.go's DailyCapExceeded subtest).
var testDailyCap = decimal.RequireFromString("100000.00")

// setupLedgerDB spins an ephemeral Postgres container via dockertest, applies
// services/ledger-service/migrations/ against it as the owner role (`ledger`),
// and returns both the owner DSN (migrations, schema assertions) and the app
// DSN (the restricted `ledger_app` role the service actually runs as).
func setupLedgerDB(t *testing.T) (ownerDSN, appDSN string) {
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
	appDSN = fmt.Sprintf("postgres://ledger_app:ledger_app@localhost:%s/ledger?sslmode=disable", hostPort)

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

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrations failed to apply: %v", err)
	}

	return ownerDSN, appDSN
}

// setupLedgerDBHighConcurrency is setupLedgerDB with a higher Postgres
// max_connections, for tests that hold 100+ simultaneous connections open
// (one per in-flight PostingRepository.Post call). The default
// max_connections=100 isn't enough headroom once the harness's own
// migration connection and Postgres's reserved superuser slots are
// subtracted — hitting that ceiling produces a flaky "sorry, too many
// clients already" failure that looks like, but isn't, the race the test
// exists to catch.
func setupLedgerDBHighConcurrency(t *testing.T) (ownerDSN, appDSN string) {
	t.Helper()

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	resource, err := pool.RunWithOptions(&dockertest.RunOptions{
		Repository: "postgres",
		Tag:        "16-alpine",
		Env: []string{
			"POSTGRES_USER=ledger",
			"POSTGRES_PASSWORD=ledger",
			"POSTGRES_DB=ledger",
		},
		Cmd: []string{"postgres", "-c", "max_connections=300"},
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
	appDSN = fmt.Sprintf("postgres://ledger_app:ledger_app@localhost:%s/ledger?sslmode=disable", hostPort)

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

	m, err := migrate.New("file://../migrations", ownerDSN+"&x-migrations-table=ledger_schema_migrations")
	if err != nil {
		t.Fatalf("could not init migrate: %v", err)
	}
	if err := m.Up(); err != nil {
		t.Fatalf("migrations failed to apply: %v", err)
	}

	return ownerDSN, appDSN
}

// newHighConcurrencyPool opens a pgxpool sized for 100+ simultaneous
// PostingRepository.Post calls against a setupLedgerDBHighConcurrency database.
func newHighConcurrencyPool(t *testing.T, appDSN string) *pgxpool.Pool {
	t.Helper()

	cfg, err := pgxpool.ParseConfig(appDSN)
	if err != nil {
		t.Fatalf("pgxpool.ParseConfig() = error %v, want nil", err)
	}
	cfg.MaxConns = 150

	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("pgxpool.NewWithConfig() = error %v, want nil", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// setupKafka spins an ephemeral single-node KRaft Kafka broker via dockertest
// and returns its host-reachable bootstrap address. KRaft's advertised
// listener must be known at container boot, so this binds a fixed host port
// rather than using dockertest's dynamic port assignment.
func setupKafka(t *testing.T) (bootstrap string) {
	t.Helper()

	pool, err := dockertest.NewPool("")
	if err != nil {
		t.Fatalf("could not connect to docker: %v", err)
	}

	const hostPort = "19092"
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

	// Explicitly provision the topic rather than relying on auto-create:
	// auto-create can leave a brief window where a producer's first request
	// still sees UNKNOWN_TOPIC_OR_PARTITION while the broker creates it
	// asynchronously. This mirrors the production kafka-topics-init step.
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

// assertLedgerSchemaHealthy asserts the full set of schema/seed/grant/privilege
// invariants ADR-004's migration set is expected to produce. Shared by the
// initial-apply test and the down/up round-trip test so both prove the exact
// same end state.
func assertLedgerSchemaHealthy(t *testing.T, ctx context.Context, ownerDSN, appDSN string) {
	t.Helper()

	conn, err := pgx.Connect(ctx, ownerDSN)
	if err != nil {
		t.Fatalf("could not connect for assertions: %v", err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, table := range []string{"ledger_transactions", "ledger_entries", "account_balances_locked", "outbox"} {
		var found int
		err := conn.QueryRow(ctx,
			"SELECT count(*) FROM information_schema.tables WHERE table_schema = 'ledger_db' AND table_name = $1",
			table).Scan(&found)
		if err != nil || found != 1 {
			t.Errorf("expected ledger_db.%s to exist, err=%v found=%d", table, err, found)
		}
	}

	var seeded int
	err = conn.QueryRow(ctx,
		"SELECT count(*) FROM ledger_db.account_balances_locked WHERE account_id = $1",
		systemFundingAccountID,
	).Scan(&seeded)
	if err != nil || seeded != 1 {
		t.Errorf("expected system funding account to be seeded, err=%v found=%d", err, seeded)
	}

	var roleExists int
	err = conn.QueryRow(ctx, "SELECT count(*) FROM pg_roles WHERE rolname = 'ledger_app'").Scan(&roleExists)
	if err != nil || roleExists != 1 {
		t.Errorf("expected ledger_app role to exist, err=%v found=%d", err, roleExists)
	}

	var createRole, createDB bool
	err = conn.QueryRow(ctx, "SELECT rolcreaterole, rolcreatedb FROM pg_roles WHERE rolname = 'ledger_app'").
		Scan(&createRole, &createDB)
	if err != nil {
		t.Errorf("could not read ledger_app privileges: %v", err)
	}
	if createRole {
		t.Error("expected ledger_app to lack CREATEROLE (ADR-004: ledger_app can never self-migrate)")
	}
	if createDB {
		t.Error("expected ledger_app to lack CREATEDB (ADR-004: ledger_app can never self-migrate)")
	}

	appConn, err := pgx.Connect(ctx, appDSN)
	if err != nil {
		t.Fatalf("could not connect as ledger_app: %v", err)
	}
	defer func() { _ = appConn.Close(ctx) }()

	assertDenied := func(name, sql string) {
		t.Helper()
		if _, err := appConn.Exec(ctx, sql); err == nil {
			t.Errorf("expected ledger_app to be denied %s, but it succeeded", name)
		}
	}
	assertAllowed := func(name, sql string) {
		t.Helper()
		if _, err := appConn.Exec(ctx, sql); err != nil {
			t.Errorf("expected ledger_app to be allowed %s, got: %v", name, err)
		}
	}

	// ledger_entries: append-only (SELECT/INSERT only — the append-only guarantee).
	assertDenied("UPDATE on ledger_entries", "UPDATE ledger_db.ledger_entries SET amount = amount")
	assertDenied("DELETE on ledger_entries", "DELETE FROM ledger_db.ledger_entries")
	assertAllowed("SELECT on ledger_entries", "SELECT * FROM ledger_db.ledger_entries")

	// account_balances_locked: SELECT/INSERT/UPDATE, but not DELETE (a lock row is never removed).
	testLockAccount := "11111111-1111-1111-1111-111111111111"
	assertAllowed("INSERT on account_balances_locked", fmt.Sprintf(
		"INSERT INTO ledger_db.account_balances_locked(account_id, balance) VALUES ('%s', 0) ON CONFLICT (account_id) DO NOTHING",
		testLockAccount))
	assertAllowed("UPDATE on account_balances_locked", fmt.Sprintf(
		"UPDATE ledger_db.account_balances_locked SET balance = balance WHERE account_id = '%s'", testLockAccount))
	assertDenied("DELETE on account_balances_locked", fmt.Sprintf(
		"DELETE FROM ledger_db.account_balances_locked WHERE account_id = '%s'", testLockAccount))

	// outbox: full CRUD (the worker needs to mark rows published and prune old ones).
	assertAllowed("INSERT on outbox", "INSERT INTO ledger_db.outbox(topic, key, payload) VALUES ('test.topic', 'grant-check', '{}'::jsonb)")
	assertAllowed("UPDATE on outbox", "UPDATE ledger_db.outbox SET published_at = now() WHERE key = 'grant-check'")
	assertAllowed("DELETE on outbox", "DELETE FROM ledger_db.outbox WHERE key = 'grant-check'")
}

// seedAccountBalance gives accountID a starting balance directly via SQL.
//
// This deliberately does not route through PostingRepository.Post: doing so
// would require debiting the seeded system funding account (balance 0.00
// out of the box — nothing in the current migrations gives it headroom),
// which checkAvailableBalance has no exemption for and immediately rejects
// as ErrInsufficientFunds. Giving the external account unlimited/negative
// balance is a Milestone 6 (deposits/withdrawals) concern, not something
// this test fixture should work around. Seeding fixture state directly also
// keeps balance setup decoupled from the exact code path under test.
func seedAccountBalance(t *testing.T, pool *pgxpool.Pool, accountID uuid.UUID, amount string) {
	t.Helper()

	if _, err := domain.NewMoney(amount); err != nil {
		t.Fatalf("seedAccountBalance: invalid amount %q: %v", amount, err)
	}

	_, err := pool.Exec(context.Background(),
		`INSERT INTO ledger_db.account_balances_locked(account_id, balance)
		 VALUES ($1, $2::numeric)
		 ON CONFLICT (account_id) DO UPDATE SET balance = EXCLUDED.balance`,
		accountID, amount,
	)
	if err != nil {
		t.Fatalf("seedAccountBalance: %v", err)
	}
}

// newBalancedTransfer builds a two-entry TRANSFER fixture with fresh UUIDs.
func newBalancedTransfer(debit, credit uuid.UUID, amount string) domain.LedgerTransaction {
	money, err := domain.NewMoney(amount)
	if err != nil {
		panic(fmt.Sprintf("newBalancedTransfer: invalid amount %q: %v", amount, err))
	}

	txID := uuid.New()
	return domain.LedgerTransaction{
		ID:   txID,
		Type: domain.TransactionTransfer,
		Entries: []domain.LedgerEntry{
			{ID: uuid.New(), TransactionID: txID, AccountID: debit, EntryType: domain.Debit, Amount: money},
			{ID: uuid.New(), TransactionID: txID, AccountID: credit, EntryType: domain.Credit, Amount: money},
		},
	}
}
