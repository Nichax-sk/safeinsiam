package database

import (
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// fakePostgres is a fake PostgreSQL server for any number of connections.
// Each accepted connection runs the server's behavior with the wire
// protocol. Behaviors close a connection only where PostgreSQL does (on a
// cancel request or a Terminate message, never for failPing); otherwise a
// connection ends only when the client closes it, or at test cleanup.
type fakePostgres struct {
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

// fakeBehavior serves one connection. It returns once a read fails.
type fakeBehavior func(b *pgproto3.Backend, conn net.Conn)

func newFakePostgres(t *testing.T, behavior fakeBehavior) *fakePostgres {
	t.Helper()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	s := &fakePostgres{port: ln.Addr().(*net.TCPAddr).Port}

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
				defer close(c.closedByClient)
				behavior(pgproto3.NewBackend(c, c), c)
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

// newFailingPingServer returns a fakePostgres that completes startup and
// then fails every query, so pgxpool keeps each connection open and idle.
func newFailingPingServer(t *testing.T) *fakePostgres {
	t.Helper()
	return newFakePostgres(t, failPing)
}

// state reports how many connections were accepted, how many the client has
// not closed, and how long ago the last one was accepted.
func (s *fakePostgres) state() (accepted, open int, quiet time.Duration) {
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

// receiveStartup reads the startup message. Like PostgreSQL, it closes a
// connection that carries a cancel request instead, and reports false.
func receiveStartup(b *pgproto3.Backend, conn net.Conn) bool {
	msg, err := b.ReceiveStartupMessage()
	if err != nil {
		return false
	}
	if _, ok := msg.(*pgproto3.CancelRequest); ok {
		conn.Close()
		return false
	}
	return true
}

// completeStartup accepts the startup message and authenticates the client.
func completeStartup(b *pgproto3.Backend, conn net.Conn) bool {
	if !receiveStartup(b, conn) {
		return false
	}
	b.Send(&pgproto3.AuthenticationOk{})
	b.Send(&pgproto3.BackendKeyData{ProcessID: 1, SecretKey: []byte{0, 0, 0, 1}})
	b.Send(&pgproto3.ReadyForQuery{TxStatus: 'I'})
	return b.Flush() == nil
}

// readUntilClosed ignores everything the client sends until it closes the
// connection, or closes it on Terminate, as PostgreSQL does.
func readUntilClosed(b *pgproto3.Backend, conn net.Conn) {
	for {
		msg, err := b.Receive()
		if err != nil {
			return
		}
		if _, ok := msg.(*pgproto3.Terminate); ok {
			conn.Close()
			return
		}
	}
}

// failPing completes startup, then answers every query with an ERROR
// followed by ReadyForQuery('I').
func failPing(b *pgproto3.Backend, conn net.Conn) {
	if !completeStartup(b, conn) {
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

// startupFatal answers the startup message with resp, a FATAL error.
func startupFatal(resp *pgproto3.ErrorResponse) fakeBehavior {
	return func(b *pgproto3.Backend, conn net.Conn) {
		if !receiveStartup(b, conn) {
			return
		}
		b.Send(resp)
		if err := b.Flush(); err != nil {
			return
		}
		readUntilClosed(b, conn)
	}
}

// hangAfterStartup completes startup and never answers a query.
func hangAfterStartup(b *pgproto3.Backend, conn net.Conn) {
	if completeStartup(b, conn) {
		readUntilClosed(b, conn)
	}
}

// hangBeforeStartup reads the startup message and never answers it.
func hangBeforeStartup(b *pgproto3.Backend, conn net.Conn) {
	if receiveStartup(b, conn) {
		readUntilClosed(b, conn)
	}
}
