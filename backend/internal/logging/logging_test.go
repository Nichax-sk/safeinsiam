package logging

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/Nichax-sk/safeinsiam/internal/config"
)

func TestNewUsesJSONInStagingAndProduction(t *testing.T) {
	for _, env := range []config.Env{config.EnvStaging, config.EnvProduction} {
		t.Run(string(env), func(t *testing.T) {
			var buf bytes.Buffer
			New(&buf, env, slog.LevelInfo).Info("hello", "key", "value")

			var entry map[string]any
			if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
				t.Fatalf("output is not JSON: %q: %v", buf.String(), err)
			}
			if entry["level"] != "INFO" || entry["msg"] != "hello" ||
				entry["key"] != "value" {
				t.Errorf("unexpected entry: %v", entry)
			}
		})
	}
}

func TestNewUsesTextInDevelopmentAndTest(t *testing.T) {
	for _, env := range []config.Env{config.EnvDevelopment, config.EnvTest} {
		t.Run(string(env), func(t *testing.T) {
			var buf bytes.Buffer
			New(&buf, env, slog.LevelInfo).Info("hello", "key", "value")

			out := buf.String()
			if json.Valid(buf.Bytes()) {
				t.Errorf("expected text output, got JSON: %q", out)
			}
			if !strings.Contains(out, "msg=hello") ||
				!strings.Contains(out, "key=value") {
				t.Errorf("unexpected text output: %q", out)
			}
		})
	}
}

func TestNewFiltersBelowLevel(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, config.EnvDevelopment, slog.LevelInfo)

	logger.Debug("hidden")
	if buf.Len() != 0 {
		t.Errorf("debug message logged at info level: %q", buf.String())
	}

	logger.Info("shown")
	if !strings.Contains(buf.String(), "shown") {
		t.Errorf("info message missing: %q", buf.String())
	}
}
