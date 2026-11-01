package app

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

func listenLocal(t *testing.T) net.Listener {
	t.Helper()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	return listener
}

// startServe runs serve in the background and returns its result channel.
func startServe(
	ctx context.Context,
	handler http.Handler,
	listener net.Listener,
	shutdownTimeout time.Duration,
) <-chan error {
	done := make(chan error, 1)
	server := &http.Server{Handler: handler}

	go func() {
		done <- serve(ctx, server, listener, shutdownTimeout, discardLogger())
	}()

	return done
}

func waitResult(t *testing.T, done <-chan error) error {
	t.Helper()

	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("serve did not return")
		return nil
	}
}

func TestServeStopsWhenContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	listener := listenLocal(t)
	done := startServe(ctx, http.NotFoundHandler(), listener, time.Second)

	resp, err := http.Get("http://" + listener.Addr().String())
	if err != nil {
		t.Fatalf("request before shutdown: %v", err)
	}
	resp.Body.Close()

	cancel()

	if err := waitResult(t, done); err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

func TestServeDrainsInFlightRequests(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})

	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(started)
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	})

	listener := listenLocal(t)
	done := startServe(ctx, handler, listener, 5*time.Second)

	respCh := make(chan *http.Response, 1)
	errCh := make(chan error, 1)
	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err != nil {
			errCh <- err
			return
		}
		respCh <- resp
	}()

	<-started
	cancel()

	select {
	case resp := <-respCh:
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("in-flight status = %d, want 200", resp.StatusCode)
		}
	case err := <-errCh:
		t.Fatalf("in-flight request failed: %v", err)
	}

	if err := waitResult(t, done); err != nil {
		t.Errorf("serve returned %v, want nil", err)
	}
}

func TestServeShutdownTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	release := make(chan struct{})
	defer close(release)

	handler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(started)
		<-release
	})

	listener := listenLocal(t)
	done := startServe(ctx, handler, listener, 50*time.Millisecond)

	go func() {
		resp, err := http.Get("http://" + listener.Addr().String())
		if err == nil {
			resp.Body.Close()
		}
	}()

	<-started
	cancel()

	err := waitResult(t, done)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("serve returned %v, want context.DeadlineExceeded", err)
	}
}

func TestRunReturnsErrorWhenContextCancelledBeforeDatabase(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cfg := &config.Config{
		Env: config.EnvTest,
		HTTP: config.HTTP{
			Host:            "127.0.0.1",
			Port:            1,
			ShutdownTimeout: time.Second,
		},
		Database: config.Database{
			Host:     "127.0.0.1",
			Port:     1,
			Name:     "app_db",
			User:     "app_user",
			Password: "unused",
			SSLMode:  "disable",
			MaxConns: 1,
		},
	}

	err := Run(ctx, cfg, discardLogger())
	if err == nil {
		t.Fatal("Run succeeded with a cancelled context")
	}
}
