//go:build integration

package database

// Integration tests against a real PostgreSQL, configured only by the
// TEST_DB_* variables. Run with:
//
//	go test -tags=integration ./...
//
// They only read: no tables, no writes, no SET.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgproto3"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

// opTimeout bounds each database operation.
const opTimeout = 5 * time.Second

var requiredTestDBVars = []string{
	"TEST_DB_HOST", "TEST_DB_PORT", "TEST_DB_NAME", "TEST_DB_USER", "TEST_DB_PASSWORD",
}

// integrationConfig returns the test database configuration from the
// TEST_DB_* variables. It fails the test, naming the variables but never
// their values, when one is missing or invalid.
func integrationConfig(t *testing.T) config.Database {
	t.Helper()

	var missing []string
	for _, name := range requiredTestDBVars {
		if os.Getenv(name) == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("integration tests need %s", strings.Join(missing, ", "))
	}

	port, err := strconv.Atoi(os.Getenv("TEST_DB_PORT"))
	if err != nil || port < 1 || port > 65535 {
		t.Fatal("TEST_DB_PORT must be a port number (1-65535)")
	}

	sslMode := os.Getenv("TEST_DB_SSLMODE")
	if sslMode == "" {
		sslMode = "disable"
	}

	return config.Database{
		Host:     os.Getenv("TEST_DB_HOST"),
		Port:     port,
		Name:     os.Getenv("TEST_DB_NAME"),
		User:     os.Getenv("TEST_DB_USER"),
		Password: os.Getenv("TEST_DB_PASSWORD"),
		SSLMode:  sslMode,
		MaxConns: 2,
		MinConns: 2,
	}
}

func jsonLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func opContext(t *testing.T) context.Context {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), opTimeout)
	t.Cleanup(cancel)
	return ctx
}

// newIntegrationPool creates a pool with NewPostgresPool and closes it when
// the test ends.
func newIntegrationPool(t *testing.T, cfg config.Database) *pgxpool.Pool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var logs bytes.Buffer
	pool, err := NewPostgresPool(ctx, cfg, jsonLogger(&logs))
	if err != nil {
		t.Fatalf("NewPostgresPool failed (%T)", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// closedPort returns a local port that was bound and released, so a dial to
// it is refused.
func closedPort(t *testing.T) int {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

func TestIntegrationPoolConnects(t *testing.T) {
	cfg := integrationConfig(t)
	pool := newIntegrationPool(t, cfg)
	ctx := opContext(t)

	if err := pool.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	var name string
	if err := pool.QueryRow(ctx, "SELECT current_database()").Scan(&name); err != nil {
		t.Fatalf("SELECT current_database(): %v", err)
	}
	if name != cfg.Name {
		t.Errorf("current_database() = %q, want TEST_DB_NAME", name)
	}

	var one int
	if err := pool.QueryRow(ctx, "SELECT 1").Scan(&one); err != nil || one != 1 {
		t.Errorf("SELECT 1 = %d, %v; want 1", one, err)
	}
}

func TestIntegrationApplicationName(t *testing.T) {
	pool := newIntegrationPool(t, integrationConfig(t))

	var name string
	if err := pool.QueryRow(opContext(t), "SHOW application_name").Scan(&name); err != nil {
		t.Fatalf("SHOW application_name: %v", err)
	}
	if name != applicationName {
		t.Errorf("application_name = %q, want %q", name, applicationName)
	}
}

func TestIntegrationStatementTimeout(t *testing.T) {
	t.Run("250ms", func(t *testing.T) {
		cfg := integrationConfig(t)
		cfg.StatementTimeout = 250 * time.Millisecond
		pool := newIntegrationPool(t, cfg)

		var setting string
		if err := pool.QueryRow(opContext(t), "SHOW statement_timeout").Scan(&setting); err != nil {
			t.Fatalf("SHOW statement_timeout: %v", err)
		}
		if setting != "250ms" {
			t.Errorf("statement_timeout = %q, want 250ms", setting)
		}

		// The Go deadline is 40 times the server timeout, so a failure here
		// can only come from PostgreSQL's statement_timeout.
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		start := time.Now()
		_, err := pool.Exec(ctx, "SELECT pg_sleep(5)")
		elapsed := time.Since(start)

		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "57014" {
			t.Fatalf("pg_sleep error is not SQLSTATE 57014: %v", err)
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			t.Error("the Go deadline fired, not statement_timeout")
		}
		if elapsed >= 5*time.Second {
			t.Errorf("pg_sleep ran %v, want it cancelled by the 250ms timeout", elapsed)
		}

		want := Classification{Kind: KindTimeout, Retryable: false, SQLState: "57014"}
		if got := Classify(err); got != want {
			t.Errorf("Classify = %+v, want %+v", got, want)
		}
	})

	t.Run("disabled", func(t *testing.T) {
		cfg := integrationConfig(t)
		cfg.StatementTimeout = 0
		pool := newIntegrationPool(t, cfg)

		var setting string
		if err := pool.QueryRow(opContext(t), "SHOW statement_timeout").Scan(&setting); err != nil {
			t.Fatalf("SHOW statement_timeout: %v", err)
		}
		// The server default; a role or database setting could change it.
		if setting != "0" {
			t.Errorf("statement_timeout = %q, want the server default 0", setting)
		}
	})
}

func TestIntegrationPoolSize(t *testing.T) {
	pool := newIntegrationPool(t, integrationConfig(t))

	if got := pool.Config(); got.MaxConns != 2 || got.MinConns != 2 {
		t.Errorf("MaxConns, MinConns = %d, %d; want 2, 2", got.MaxConns, got.MinConns)
	}

	// MinConns connections are established in the background.
	deadline := time.Now().Add(opTimeout)
	for pool.Stat().TotalConns() < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("TotalConns = %d after %v, want 2", pool.Stat().TotalConns(), opTimeout)
		}
		time.Sleep(20 * time.Millisecond)
	}

	ctx := opContext(t)
	for range 2 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		defer conn.Release()
	}

	short, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if conn, err := pool.Acquire(short); !errors.Is(err, context.DeadlineExceeded) {
		if conn != nil {
			conn.Release()
		}
		t.Errorf("third Acquire error = %v, want context.DeadlineExceeded (MaxConns 2)", err)
	}
}

func TestIntegrationPoolCloses(t *testing.T) {
	cfg := integrationConfig(t)
	pool := newIntegrationPool(t, cfg)
	ctx := opContext(t)

	var pids []int32
	var conns []*pgxpool.Conn
	for range 2 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire: %v", err)
		}
		var pid int32
		if err := conn.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			t.Fatalf("SELECT pg_backend_pid(): %v", err)
		}
		pids = append(pids, pid)
		conns = append(conns, conn)
	}
	for _, conn := range conns {
		conn.Release()
	}

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		pool.Close()
	}()
	select {
	case <-closed:
	case <-time.After(opTimeout):
		t.Fatalf("pool.Close did not return within %v", opTimeout)
	}

	// The observer is a separate pool; filtering by PID keeps it independent
	// of other tests' sessions.
	observer := newIntegrationPool(t, cfg)
	deadline := time.Now().Add(opTimeout)
	for {
		var remaining int
		err := observer.QueryRow(ctx,
			"SELECT count(*) FROM pg_stat_activity WHERE pid = ANY($1)", pids,
		).Scan(&remaining)
		if err != nil {
			t.Fatalf("query pg_stat_activity: %v", err)
		}
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of the pool's %d sessions still in pg_stat_activity after %v",
				remaining, len(pids), opTimeout)
		}
		time.Sleep(50 * time.Millisecond)
	}

	if err := pool.Ping(ctx); err == nil {
		t.Error("Ping on a closed pool succeeded")
	}
}

func TestIntegrationUnreachableTargetFails(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.Host = "127.0.0.1"
	cfg.Port = closedPort(t)

	t.Run("full retry policy", func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		start := time.Now()
		pool, err := NewPostgresPool(ctx, cfg, jsonLogger(&logs))
		elapsed := time.Since(start)

		if pool != nil {
			pool.Close()
			t.Fatal("NewPostgresPool returned a pool for an unreachable target")
		}
		if err == nil {
			t.Fatal("NewPostgresPool returned no error for an unreachable target")
		}

		var connectErr *pgconn.ConnectError
		if !errors.As(err, &connectErr) {
			t.Errorf("error chain has no *pgconn.ConnectError (%T)", err)
		}
		want := Classification{Kind: KindUnavailable, Retryable: true}
		if got := Classify(err); got != want {
			t.Errorf("Classify = %+v, want %+v", got, want)
		}

		// The unmodified backoff waits 0.5s + 1s + 2s + 4s between 5 attempts.
		if elapsed < 7*time.Second {
			t.Errorf("NewPostgresPool returned after %v, want the full retry policy (>= 7s)", elapsed)
		}

		retries := 0
		for _, line := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
			var entry map[string]any
			if err := json.Unmarshal([]byte(line), &entry); err == nil &&
				entry["msg"] == "database not ready, retrying" {
				retries++
			}
		}
		if retries != 4 {
			t.Errorf("logged %d retries, want 4", retries)
		}
		if strings.Contains(logs.String(), cfg.Password) {
			t.Error("the log contains TEST_DB_PASSWORD")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		var logs bytes.Buffer
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()

		start := time.Now()
		pool, err := NewPostgresPool(ctx, cfg, jsonLogger(&logs))
		elapsed := time.Since(start)

		if pool != nil {
			pool.Close()
			t.Fatal("NewPostgresPool returned a pool for an unreachable target")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("error is not context.DeadlineExceeded (%T)", err)
		}
		if elapsed >= 5*time.Second {
			t.Errorf("NewPostgresPool returned after %v, want it to stop at the 1s deadline", elapsed)
		}
	})
}

// failingPingServer is a fake PostgreSQL server for many connections. It
// completes startup and then fails every query with an ERROR followed by
// ReadyForQuery('I'), so pgxpool keeps each connection open and idle. It
// never closes a connection first: a connection ends only when the client
// closes it, or at test cleanup.
type failingPingServer struct {
	port int

	mu         sync.Mutex
	conns      []*fakeServerConn
	lastAccept time.Time
}

type fakeServerConn struct {
	net.Conn
	// closedByClient is closed once a read fails: the client closed the
	// connection.
	closedByClient chan struct{}
}

func newFailingPingServer(t *testing.T) *failingPingServer {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &failingPingServer{port: ln.Addr().(*net.TCPAddr).Port}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			c := &fakeServerConn{Conn: conn, closedByClient: make(chan struct{})}

			s.mu.Lock()
			s.conns = append(s.conns, c)
			s.lastAccept = time.Now()
			s.mu.Unlock()

			wg.Add(1)
			go func() {
				defer wg.Done()
				s.serve(c)
			}()
		}
	}()

	t.Cleanup(func() {
		ln.Close()
		s.mu.Lock()
		for _, c := range s.conns {
			c.Close()
		}
		s.mu.Unlock()
		wg.Wait()
	})

	return s
}

func (s *failingPingServer) serve(c *fakeServerConn) {
	defer close(c.closedByClient)

	b := pgproto3.NewBackend(c, c)
	if _, err := b.ReceiveStartupMessage(); err != nil {
		return
	}
	b.Send(&pgproto3.AuthenticationOk{})
	b.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	if err := b.Flush(); err != nil {
		return
	}

	for {
		msg, err := b.Receive()
		if err != nil {
			return
		}
		switch msg.(type) {
		case *pgproto3.Query, *pgproto3.Sync:
			b.Send(&pgproto3.ErrorResponse{Severity: "ERROR", Code: "XX000", Message: "ping rejected"})
			b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
			if err := b.Flush(); err != nil {
				return
			}
		}
		// Terminate and anything else: keep reading until the client
		// closes the connection.
	}
}

// state reports how many connections were accepted, how many the client has
// not closed, and how long ago the last one was accepted.
func (s *failingPingServer) state() (accepted, open int, quiet time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, c := range s.conns {
		select {
		case <-c.closedByClient:
		default:
			open++
		}
	}
	return len(s.conns), open, time.Since(s.lastAccept)
}

// TestIntegrationFailedPoolClosesSockets checks that a pool whose startup
// ping fails closes every socket it opened. It proves only that those
// sockets were closed by the client, not that every goroutine or timer was
// released. It needs no real database.
//
// Mutation check: in an overlay copy of postgres.go, delete only the
// pool.Close() in the retry-failure branch of NewPostgresPool:
//
//	if err := retry(...); err != nil {
//		pool.Close() // delete this line
//		return nil, fmt.Errorf("connect to postgres: %w", err)
//	}
//
// then run
//
//	go test -tags=integration -overlay overlay.json \
//		-run TestIntegrationFailedPoolClosesSockets ./internal/database
//
// The test must fail with connections still open; it passes on the real file.
func TestIntegrationFailedPoolClosesSockets(t *testing.T) {
	server := newFailingPingServer(t)
	cfg := config.Database{
		Host:     "127.0.0.1",
		Port:     server.port,
		Name:     "fake_db",
		User:     "fake_user",
		Password: "fake_password",
		SSLMode:  "disable",
		MaxConns: 2,
		MinConns: 2,
	}

	var logs bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := NewPostgresPool(ctx, cfg, jsonLogger(&logs))
	if pool != nil {
		pool.Close()
		t.Fatal("NewPostgresPool returned a pool although every ping failed")
	}

	// XX000 proves the connections authenticated and the ping query failed.
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "XX000" {
		t.Fatalf("NewPostgresPool error is not the rejected ping (SQLSTATE XX000), got %T", err)
	}

	if accepted, _, _ := server.state(); accepted == 0 {
		t.Fatal("the fake server accepted no connections")
	}

	// Every accepted connection must be closed by the client, with no new
	// connection for 1s, which catches a late background MinConns connection.
	deadline := time.Now().Add(opTimeout)
	for {
		accepted, open, quiet := server.state()
		if open == 0 && quiet >= time.Second {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d connections still open after %v", open, accepted, opTimeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
