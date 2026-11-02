package config

import (
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const testPassword = "s3cret-Pa55word"

func lookupFrom(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func requiredVars(extra map[string]string) map[string]string {
	values := map[string]string{
		"POSTGRES_DB":       "app_db",
		"POSTGRES_USER":     "app_user",
		"POSTGRES_PASSWORD": testPassword,
	}
	for key, value := range extra {
		values[key] = value
	}

	return values
}

func TestParseDefaults(t *testing.T) {
	cfg, err := parse(lookupFrom(requiredVars(nil)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	want := Config{
		Env:      EnvDevelopment,
		LogLevel: slog.LevelDebug,
		HTTP: HTTP{
			Host:            "127.0.0.1",
			Port:            8080,
			ShutdownTimeout: 10 * time.Second,
		},
		Database: Database{
			Host:             "127.0.0.1",
			Port:             5432,
			Name:             "app_db",
			User:             "app_user",
			Password:         testPassword,
			SSLMode:          "disable",
			MaxConns:         10,
			MinConns:         2,
			StatementTimeout: 30 * time.Second,
		},
	}
	if !reflect.DeepEqual(*cfg, want) {
		t.Errorf("parse defaults:\n got %+v\nwant %+v", *cfg, want)
	}
}

func TestParseCustomValues(t *testing.T) {
	cfg, err := parse(lookupFrom(requiredVars(map[string]string{
		"APP_ENV":              "staging",
		"APP_HOST":             "0.0.0.0",
		"APP_PORT":             "9090",
		"LOG_LEVEL":            "warn",
		"SHUTDOWN_TIMEOUT":     "25s",
		"DB_HOST":              "db.internal",
		"POSTGRES_PORT":        "6543",
		"DB_SSLMODE":           "verify-full",
		"DB_MAX_CONNS":         "20",
		"DB_MIN_CONNS":         "0",
		"DB_STATEMENT_TIMEOUT": "0",
	})))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if cfg.Env != EnvStaging || cfg.HTTP.Host != "0.0.0.0" ||
		cfg.HTTP.Port != 9090 || cfg.LogLevel != slog.LevelWarn ||
		cfg.HTTP.ShutdownTimeout != 25*time.Second {
		t.Errorf("unexpected app config: %+v", cfg)
	}
	db := cfg.Database
	if db.Host != "db.internal" || db.Port != 6543 ||
		db.SSLMode != "verify-full" || db.MaxConns != 20 ||
		db.MinConns != 0 || db.StatementTimeout != 0 {
		t.Errorf("unexpected database config: %+v", db)
	}
}

func TestParseDefaultLogLevelByEnv(t *testing.T) {
	tests := map[Env]slog.Level{
		EnvDevelopment: slog.LevelDebug,
		EnvTest:        slog.LevelInfo,
		EnvStaging:     slog.LevelInfo,
		EnvProduction:  slog.LevelInfo,
	}

	for env, want := range tests {
		t.Run(string(env), func(t *testing.T) {
			cfg, err := parse(lookupFrom(requiredVars(map[string]string{
				"APP_ENV":    string(env),
				"DB_SSLMODE": "require",
			})))
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if cfg.LogLevel != want {
				t.Errorf("LogLevel = %v, want %v", cfg.LogLevel, want)
			}
		})
	}
}

func TestParseInvalidValues(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{"unknown env", "APP_ENV", "prod", "invalid APP_ENV"},
		{"non-numeric app port", "APP_PORT", "abc", "invalid APP_PORT"},
		{"zero app port", "APP_PORT", "0", "invalid APP_PORT"},
		{"app port too large", "APP_PORT", "70000", "invalid APP_PORT"},
		{"non-numeric db port", "POSTGRES_PORT", "x", "invalid POSTGRES_PORT"},
		{"unknown ssl mode", "DB_SSLMODE", "bogus", "invalid DB_SSLMODE"},
		{"unknown log level", "LOG_LEVEL", "loud", "invalid LOG_LEVEL"},
		{"shutdown without unit", "SHUTDOWN_TIMEOUT", "10", "invalid SHUTDOWN_TIMEOUT"},
		{"negative shutdown", "SHUTDOWN_TIMEOUT", "-1s", "invalid SHUTDOWN_TIMEOUT"},
		{"zero shutdown", "SHUTDOWN_TIMEOUT", "0s", "invalid SHUTDOWN_TIMEOUT"},
		{"zero max conns", "DB_MAX_CONNS", "0", "invalid DB_MAX_CONNS"},
		{"non-numeric max conns", "DB_MAX_CONNS", "many", "invalid DB_MAX_CONNS"},
		{"negative min conns", "DB_MIN_CONNS", "-1", "invalid DB_MIN_CONNS"},
		{"min above max", "DB_MIN_CONNS", "11", "must not exceed DB_MAX_CONNS"},
		{"negative statement timeout", "DB_STATEMENT_TIMEOUT", "-1s", "invalid DB_STATEMENT_TIMEOUT"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parse(lookupFrom(requiredVars(map[string]string{
				tt.key: tt.value,
			})))
			if err == nil {
				t.Fatalf("parse succeeded with %s=%q", tt.key, tt.value)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestParseReportsAllErrors(t *testing.T) {
	_, err := parse(lookupFrom(map[string]string{
		"APP_ENV":    "prod",
		"APP_PORT":   "abc",
		"DB_SSLMODE": "bogus",
	}))
	if err == nil {
		t.Fatal("parse succeeded with invalid configuration")
	}

	for _, want := range []string{
		"invalid APP_ENV",
		"invalid APP_PORT",
		"invalid DB_SSLMODE",
		"POSTGRES_DB is required",
		"POSTGRES_USER is required",
		"POSTGRES_PASSWORD is required",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestParseProductionSSLMode(t *testing.T) {
	tests := map[string]bool{
		"disable":     false,
		"allow":       false,
		"prefer":      false,
		"require":     true,
		"verify-ca":   true,
		"verify-full": true,
	}

	for mode, allowed := range tests {
		t.Run(mode, func(t *testing.T) {
			_, err := parse(lookupFrom(requiredVars(map[string]string{
				"APP_ENV":    "production",
				"DB_SSLMODE": mode,
			})))
			if allowed && err != nil {
				t.Errorf("DB_SSLMODE=%s rejected in production: %v", mode, err)
			}
			if !allowed && err == nil {
				t.Errorf("DB_SSLMODE=%s accepted in production", mode)
			}
		})
	}
}

func TestParseErrorsNeverContainPassword(t *testing.T) {
	_, err := parse(lookupFrom(requiredVars(map[string]string{
		"APP_ENV":      "prod",
		"APP_PORT":     "abc",
		"DB_SSLMODE":   "bogus",
		"DB_MAX_CONNS": "0",
	})))
	if err == nil {
		t.Fatal("parse succeeded with invalid configuration")
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error exposes the database password: %q", err)
	}
}

// clearEnv unsets every variable Load reads, restoring them after the test.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, key := range []string{
		"ENV_FILE", "APP_ENV", "APP_HOST", "APP_PORT", "LOG_LEVEL",
		"SHUTDOWN_TIMEOUT", "DB_HOST", "POSTGRES_PORT", "POSTGRES_DB",
		"POSTGRES_USER", "POSTGRES_PASSWORD", "DB_SSLMODE", "DB_MAX_CONNS",
		"DB_MIN_CONNS", "DB_STATEMENT_TIMEOUT",
	} {
		t.Setenv(key, "")
		os.Unsetenv(key)
	}
}

func writeEnvFile(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	return path
}

const envFileContent = `POSTGRES_DB=file_db
POSTGRES_USER=file_user
POSTGRES_PASSWORD=file_password
APP_PORT=7000
DB_HOST=file-host
`

func TestLoadPrecedence(t *testing.T) {
	clearEnv(t)
	path := writeEnvFile(t, envFileContent)
	t.Setenv("ENV_FILE", path)
	t.Setenv("APP_PORT", "9000")
	t.Setenv("DB_HOST", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if cfg.EnvFile != path {
		t.Errorf("EnvFile = %q, want %q", cfg.EnvFile, path)
	}
	if cfg.HTTP.Port != 9000 {
		t.Errorf("process env should win: APP_PORT = %d, want 9000", cfg.HTTP.Port)
	}
	if cfg.Database.Name != "file_db" {
		t.Errorf("file should supply unset values: POSTGRES_DB = %q", cfg.Database.Name)
	}
	if cfg.Database.Host != "file-host" {
		t.Errorf("empty process value should fall through to file: DB_HOST = %q",
			cfg.Database.Host)
	}
	if cfg.HTTP.Host != "127.0.0.1" {
		t.Errorf("default should apply when unset everywhere: APP_HOST = %q",
			cfg.HTTP.Host)
	}
	if got := os.Getenv("POSTGRES_DB"); got != "" {
		t.Errorf("Load modified the process environment: POSTGRES_DB = %q", got)
	}
}

func TestLoadMissingDefaultEnvFileIsIgnored(t *testing.T) {
	clearEnv(t)
	// The default ../.env does not exist relative to this package directory.
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnvFile != "" {
		t.Errorf("EnvFile = %q, want empty", cfg.EnvFile)
	}
}

func TestLoadMissingExplicitEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV_FILE", filepath.Join(t.TempDir(), "missing.env"))

	if _, err := Load(); err == nil {
		t.Fatal("Load succeeded with a missing explicit ENV_FILE")
	}
}

func TestLoadProductionSkipsEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_SSLMODE", "require")
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.EnvFile != "" {
		t.Errorf("EnvFile = %q, want empty in production", cfg.EnvFile)
	}
}

const productionEnvFileContent = `APP_ENV=production
DB_SSLMODE=require
POSTGRES_DB=file_db
POSTGRES_USER=file_user
POSTGRES_PASSWORD=file-only-s3cret
`

// chdirBelowEnvFile writes content to <tmp>/.env and changes into
// <tmp>/backend, so the default ../.env path resolves to that file.
func chdirBelowEnvFile(t *testing.T, content string) string {
	t.Helper()

	root := t.TempDir()
	path := filepath.Join(root, ".env")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}

	workDir := filepath.Join(root, "backend")
	if err := os.Mkdir(workDir, 0o700); err != nil {
		t.Fatalf("create working directory: %v", err)
	}
	t.Chdir(workDir)

	return path
}

func TestLoadRejectsProductionFromEnvFile(t *testing.T) {
	tests := map[string]func(t *testing.T){
		"explicit ENV_FILE": func(t *testing.T) {
			t.Setenv("ENV_FILE", writeEnvFile(t, productionEnvFileContent))
		},
		"default ../.env": func(t *testing.T) {
			chdirBelowEnvFile(t, productionEnvFileContent)
		},
	}

	for name, setup := range tests {
		t.Run(name, func(t *testing.T) {
			clearEnv(t)
			setup(t)

			cfg, err := Load()
			if err == nil {
				t.Fatalf("Load accepted APP_ENV=production from an env file (Env=%q)", cfg.Env)
			}
			if cfg != nil {
				t.Errorf("Load returned a config alongside the error")
			}
			for _, want := range []string{"APP_ENV=production", "process environment"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "file-only-s3cret") {
				t.Errorf("error exposes the database password: %q", err)
			}
		})
	}
}

func TestLoadProcessEnvOverridesProductionInEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("ENV_FILE", writeEnvFile(t, productionEnvFileContent))
	t.Setenv("APP_ENV", "staging")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvStaging {
		t.Errorf("Env = %q, want %q", cfg.Env, EnvStaging)
	}
}

func TestLoadProductionFromProcessIgnoresEnvFile(t *testing.T) {
	clearEnv(t)
	chdirBelowEnvFile(t, productionEnvFileContent)
	t.Setenv("APP_ENV", "production")
	t.Setenv("DB_SSLMODE", "verify-full")
	t.Setenv("POSTGRES_DB", "app_db")
	t.Setenv("POSTGRES_USER", "app_user")
	t.Setenv("POSTGRES_PASSWORD", testPassword)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Env != EnvProduction || cfg.EnvFile != "" {
		t.Errorf("Env = %q, EnvFile = %q; want production and no env file",
			cfg.Env, cfg.EnvFile)
	}
	db := cfg.Database
	if db.Name != "app_db" || db.User != "app_user" ||
		db.Password != testPassword || db.SSLMode != "verify-full" {
		t.Errorf("database config not taken from the process environment: "+
			"name=%q user=%q sslmode=%q", db.Name, db.User, db.SSLMode)
	}
}

func TestLoadProductionRejectsEnvFile(t *testing.T) {
	clearEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("ENV_FILE", writeEnvFile(t, envFileContent))

	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "ENV_FILE") {
		t.Fatalf("Load error = %v, want ENV_FILE rejection", err)
	}
}

func parseHTTP(t *testing.T, extra map[string]string) (HTTP, error) {
	t.Helper()

	cfg, err := parse(lookupFrom(requiredVars(extra)))
	if err != nil {
		return HTTP{}, err
	}
	return cfg.HTTP, nil
}

func TestParseCORSAndProxiesDisabledByDefault(t *testing.T) {
	httpCfg, err := parseHTTP(t, nil)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if httpCfg.CORSAllowedOrigins != nil || httpCfg.TrustedProxies != nil {
		t.Errorf("CORS origins = %v, proxies = %v; want both nil",
			httpCfg.CORSAllowedOrigins, httpCfg.TrustedProxies)
	}
}

func TestParseCORSOriginsCanonicalizes(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"http://localhost:3000", []string{"http://localhost:3000"}},
		{"http://localhost:80", []string{"http://localhost"}},
		{"https://example.test:443", []string{"https://example.test"}},
		{"HTTPS://Example.TEST:443/", []string{"https://example.test"}},
		{"https://example.test:8443", []string{"https://example.test:8443"}},
		{"http://example.test:443", []string{"http://example.test:443"}},
		{"https://example.test:80", []string{"https://example.test:80"}},
		{"http://[::1]:80", []string{"http://[::1]"}},
		{"http://[::1]:3000", []string{"http://[::1]:3000"}},
		{"http://LOCALHOST:3000/", []string{"http://localhost:3000"}},
		{"http://localhost:3000,http://LOCALHOST:3000/", []string{"http://localhost:3000"}},
		{" http://a.test , https://b.test:8443 ,", []string{"http://a.test", "https://b.test:8443"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			httpCfg, err := parseHTTP(t, map[string]string{"CORS_ALLOWED_ORIGINS": tt.input})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !slices.Equal(httpCfg.CORSAllowedOrigins, tt.want) {
				t.Errorf("origins = %v, want %v", httpCfg.CORSAllowedOrigins, tt.want)
			}
		})
	}
}

func TestParseCORSOriginsRejectsInvalid(t *testing.T) {
	for _, input := range []string{
		"*", "null", "NULL", "http://*.example.test",
		"http://localhost:3000/app", "http://localhost:3000?x=1", "http://localhost:3000#x",
		"http://user:pass@localhost:3000", "ftp://localhost", "localhost:3000",
		"example.test", "http://", "http://localhost:0", "http://localhost:70000",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := parseHTTP(t, map[string]string{"CORS_ALLOWED_ORIGINS": input})
			if err == nil || !strings.Contains(err.Error(), "CORS_ALLOWED_ORIGINS") {
				t.Errorf("parse error = %v, want CORS_ALLOWED_ORIGINS rejection", err)
			}
		})
	}
}

func TestParseCORSOriginsProductionRequiresHTTPS(t *testing.T) {
	production := map[string]string{"APP_ENV": "production", "DB_SSLMODE": "require"}

	production["CORS_ALLOWED_ORIGINS"] = "http://app.example.test"
	if _, err := parseHTTP(t, production); err == nil ||
		!strings.Contains(err.Error(), "https in production") {
		t.Errorf("parse error = %v, want https-in-production rejection", err)
	}

	production["CORS_ALLOWED_ORIGINS"] = "https://app.example.test"
	httpCfg, err := parseHTTP(t, production)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !slices.Equal(httpCfg.CORSAllowedOrigins, []string{"https://app.example.test"}) {
		t.Errorf("origins = %v", httpCfg.CORSAllowedOrigins)
	}
}

func TestParseTrustedProxiesCanonicalizes(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"10.0.0.0/8", []string{"10.0.0.0/8"}},
		{"172.16.0.0/12,192.168.1.10", []string{"172.16.0.0/12", "192.168.1.10/32"}},
		{"fd00::/8", []string{"fd00::/8"}},
		{"2001:db8::1", []string{"2001:db8::1/128"}},
		// IPv4-mapped forms are written as IPv4, as net.IPNet.Contains sees them.
		{"::ffff:10.0.0.0/104", []string{"10.0.0.0/8"}},
		{"::ffff:192.0.2.7", []string{"192.0.2.7/32"}},
		// Incomplete coverage stays valid.
		{"0.0.0.0/1", []string{"0.0.0.0/1"}},
		{"0.0.0.0/1,192.0.0.0/2", []string{"0.0.0.0/1", "192.0.0.0/2"}},
		{"::/1", []string{"::/1"}},
		{"::/1,c000::/2", []string{"::/1", "c000::/2"}},
		{"0.0.0.0/1,8000::/1", []string{"0.0.0.0/1", "8000::/1"}},
		{"10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fd00::/8",
			[]string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16", "fd00::/8"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			httpCfg, err := parseHTTP(t, map[string]string{"TRUSTED_PROXIES": tt.input})
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if !slices.Equal(httpCfg.TrustedProxies, tt.want) {
				t.Errorf("proxies = %v, want %v", httpCfg.TrustedProxies, tt.want)
			}
		})
	}
}

func TestParseTrustedProxiesRejectsInvalid(t *testing.T) {
	for _, input := range []string{"nope", "10.0.0.0/33", "1.2.3", "10.0.0.0/8,garbage"} {
		t.Run(input, func(t *testing.T) {
			_, err := parseHTTP(t, map[string]string{"TRUSTED_PROXIES": input})
			if err == nil || !strings.Contains(err.Error(), "invalid TRUSTED_PROXIES entry") {
				t.Errorf("parse error = %v, want an invalid-entry error", err)
			}
		})
	}
}

func TestParseTrustedProxiesRejectsTrustAll(t *testing.T) {
	tests := []struct {
		input  string
		family string
	}{
		// Single entries (R2).
		{"0.0.0.0/0", "IPv4"},
		{"::/0", "IPv6"},
		{"::ffff:0:0/96", "IPv4"},
		{"::ffff:0.0.0.0/96", "IPv4"},
		// Aggregate coverage (R4).
		{"0.0.0.0/1,128.0.0.0/1", "IPv4"},
		{"128.0.0.0/1,0.0.0.0/1", "IPv4"},
		{"::/1,8000::/1", "IPv6"},
		{"0.0.0.0/2,64.0.0.0/2,128.0.0.0/1", "IPv4"},
		{"::ffff:0:0/97,::ffff:128.0.0.0/97", "IPv4"},
		{"0.0.0.0/1,10.0.0.0/8,128.0.0.0/1", "IPv4"},
		{"::/1,8000::/1,10.0.0.0/8", "IPv6"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			_, err := parseHTTP(t, map[string]string{"TRUSTED_PROXIES": tt.input})
			want := "covers all " + tt.family + " addresses"
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("parse error = %v, want %q", err, want)
			}
		})
	}
}

// The trust-all rule targets what Gin actually trusts: each rejected single
// network really does contain every address of its family per Contains.
func TestTrustAllNetworksMatchContains(t *testing.T) {
	for _, cidr := range []string{"0.0.0.0/0", "::ffff:0:0/96", "::ffff:0.0.0.0/96"} {
		_, network, _ := net.ParseCIDR(cidr)
		for _, ip := range []string{"0.0.0.0", "1.2.3.4", "255.255.255.255"} {
			if !network.Contains(net.ParseIP(ip)) {
				t.Errorf("%s does not contain %s", cidr, ip)
			}
		}
	}
	_, all6, _ := net.ParseCIDR("::/0")
	if !all6.Contains(net.ParseIP("2001:db8::1")) {
		t.Error("::/0 does not contain 2001:db8::1")
	}
}

func TestPrefixTrie(t *testing.T) {
	half := func(first byte) net.IP { return net.IP{first, 0, 0, 0} }

	var empty prefixNode
	if empty.covers() {
		t.Error("empty trie covers")
	}

	var all prefixNode
	all.insert(half(0), 0)
	if !all.covers() {
		t.Error("/0 does not cover")
	}

	for _, order := range [][2]byte{{0x00, 0x80}, {0x80, 0x00}} {
		var trie prefixNode
		trie.insert(half(order[0]), 1)
		trie.insert(half(order[1]), 1)
		if !trie.covers() {
			t.Errorf("halves inserted in order %v do not cover", order)
		}
	}

	var redundant prefixNode
	redundant.insert(half(0x00), 1)
	redundant.insert(half(0x0a), 8) // inside 0.0.0.0/1
	if redundant.covers() || !redundant.child[0].full || redundant.child[0].child[0] != nil {
		t.Error("a longer prefix under a covered one changed the trie")
	}

	var replaced prefixNode
	replaced.insert(half(0x0a), 8)
	replaced.insert(half(0x00), 1) // replaces the /8 below it
	replaced.insert(half(0x80), 1)
	if !replaced.covers() {
		t.Error("shorter prefixes replacing a longer one do not cover")
	}

	var deep prefixNode
	deep.insert(net.ParseIP("2001:db8::1").To16(), 128)
	if deep.covers() {
		t.Error("a single /128 covers all IPv6")
	}
}

func TestNetworkFamily(t *testing.T) {
	tests := []struct {
		cidr       string
		wantIPv4   bool
		wantPrefix int
	}{
		{"10.0.0.0/8", true, 8},
		{"::ffff:10.0.0.0/104", true, 8},
		{"::ffff:0:0/96", true, 0},
		{"::ffff:0:0/95", false, 95},
		{"fd00::/8", false, 8},
	}

	for _, tt := range tests {
		_, network, err := net.ParseCIDR(tt.cidr)
		if err != nil {
			t.Fatalf("ParseCIDR(%s): %v", tt.cidr, err)
		}
		_, prefix, isIPv4 := networkFamily(network)
		if isIPv4 != tt.wantIPv4 || prefix != tt.wantPrefix {
			t.Errorf("%s: ipv4=%v prefix=%d, want ipv4=%v prefix=%d",
				tt.cidr, isIPv4, prefix, tt.wantIPv4, tt.wantPrefix)
		}
	}
}

func TestParseReportsCORSAndProxyErrorsTogether(t *testing.T) {
	_, err := parseHTTP(t, map[string]string{
		"CORS_ALLOWED_ORIGINS": "*",
		"TRUSTED_PROXIES":      "0.0.0.0/0",
	})
	if err == nil {
		t.Fatal("parse succeeded")
	}
	for _, want := range []string{"CORS_ALLOWED_ORIGINS", "TRUSTED_PROXIES"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %s", err, want)
		}
	}
}
