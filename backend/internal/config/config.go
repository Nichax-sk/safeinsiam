package config

import (
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

type Env string

const (
	EnvDevelopment Env = "development"
	EnvTest        Env = "test"
	EnvStaging     Env = "staging"
	EnvProduction  Env = "production"
)

const defaultEnvFile = "../.env"

var validEnvs = []Env{EnvDevelopment, EnvTest, EnvStaging, EnvProduction}

var validSSLModes = []string{
	"disable", "allow", "prefer", "require", "verify-ca", "verify-full",
}

// insecureSSLModes are rejected in production because they permit
// unencrypted connections.
var insecureSSLModes = []string{"disable", "allow", "prefer"}

type Config struct {
	Env Env
	// EnvFile is the path of the environment file that was read, or ""
	// when none was read.
	EnvFile  string
	LogLevel slog.Level

	HTTP     HTTP
	Database Database
}

type HTTP struct {
	Host            string
	Port            int
	ShutdownTimeout time.Duration

	// CORSAllowedOrigins are canonical origins (lowercase, no default port)
	// allowed to read API responses from a browser. Empty disables CORS.
	CORSAllowedOrigins []string
	// TrustedProxies are canonical CIDRs of proxies whose X-Forwarded-For
	// header is trusted for the client IP. Empty trusts no proxy.
	TrustedProxies []string
}

type Database struct {
	Host     string
	Port     int
	Name     string
	User     string
	Password string
	SSLMode  string

	MaxConns         int32
	MinConns         int32
	StatementTimeout time.Duration
}

// Load reads configuration from the process environment and, outside
// production, from an environment file (ENV_FILE, default ../.env
// relative to the working directory). Non-empty process variables take
// precedence over file values. The process environment is not modified.
func Load() (*Config, error) {
	envFile, fileValues, err := readEnvFile()
	if err != nil {
		return nil, err
	}

	lookup := func(key string) (string, bool) {
		if value := os.Getenv(key); value != "" {
			return value, true
		}
		if value := fileValues[key]; value != "" {
			return value, true
		}
		return "", false
	}

	// Production must be selected by the process environment. readEnvFile
	// never reads a file when it is, so production here means the file
	// supplied APP_ENV.
	if value, _ := lookup("APP_ENV"); Env(value) == EnvProduction && envFile != "" {
		return nil, fmt.Errorf(
			"APP_ENV=production was read from environment file %s: "+
				"set APP_ENV in the process environment; "+
				"environment files are not read in production",
			envFile,
		)
	}

	cfg, err := parse(lookup)
	if err != nil {
		return nil, err
	}
	cfg.EnvFile = envFile

	return cfg, nil
}

func readEnvFile() (string, map[string]string, error) {
	path, explicit := os.LookupEnv("ENV_FILE")

	if Env(os.Getenv("APP_ENV")) == EnvProduction {
		if explicit {
			return "", nil, errors.New(
				"ENV_FILE must not be set when APP_ENV=production",
			)
		}
		return "", nil, nil
	}

	if !explicit || path == "" {
		path = defaultEnvFile
		explicit = false
	}

	values, err := godotenv.Read(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) && !explicit {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("read environment file %s: %w", path, err)
	}

	return path, values, nil
}

// parse builds and validates a Config from lookup, reporting every
// invalid value at once. It never includes POSTGRES_PASSWORD in errors.
func parse(lookup func(string) (string, bool)) (*Config, error) {
	var errs []error

	get := func(key, fallback string) string {
		if value, ok := lookup(key); ok && value != "" {
			return value
		}
		return fallback
	}

	cfg := &Config{
		Env: Env(get("APP_ENV", string(EnvDevelopment))),
		HTTP: HTTP{
			Host: get("APP_HOST", "127.0.0.1"),
		},
		Database: Database{
			Host:     get("DB_HOST", "127.0.0.1"),
			Name:     get("POSTGRES_DB", ""),
			User:     get("POSTGRES_USER", ""),
			Password: get("POSTGRES_PASSWORD", ""),
			SSLMode:  get("DB_SSLMODE", "disable"),
		},
	}

	if !slices.Contains(validEnvs, cfg.Env) {
		errs = append(errs, fmt.Errorf(
			"invalid APP_ENV %q: must be one of %v", cfg.Env, validEnvs,
		))
	}

	defaultLogLevel := "info"
	if cfg.Env == EnvDevelopment {
		defaultLogLevel = "debug"
	}
	logLevel := get("LOG_LEVEL", defaultLogLevel)
	if err := cfg.LogLevel.UnmarshalText([]byte(logLevel)); err != nil {
		errs = append(errs, fmt.Errorf(
			"invalid LOG_LEVEL %q: must be debug, info, warn or error", logLevel,
		))
	}

	var err error
	if cfg.HTTP.Port, err = parsePort("APP_PORT", get("APP_PORT", "8080")); err != nil {
		errs = append(errs, err)
	}
	if cfg.HTTP.ShutdownTimeout, err = parseDuration(
		"SHUTDOWN_TIMEOUT", get("SHUTDOWN_TIMEOUT", "10s"), false,
	); err != nil {
		errs = append(errs, err)
	}

	if cfg.HTTP.CORSAllowedOrigins, err = parseOrigins(
		get("CORS_ALLOWED_ORIGINS", ""), cfg.Env == EnvProduction,
	); err != nil {
		errs = append(errs, err)
	}
	if cfg.HTTP.TrustedProxies, err = parseTrustedProxies(
		get("TRUSTED_PROXIES", ""),
	); err != nil {
		errs = append(errs, err)
	}

	if cfg.Database.Port, err = parsePort(
		"POSTGRES_PORT", get("POSTGRES_PORT", "5432"),
	); err != nil {
		errs = append(errs, err)
	}

	for _, required := range []struct{ key, value string }{
		{"POSTGRES_DB", cfg.Database.Name},
		{"POSTGRES_USER", cfg.Database.User},
		{"POSTGRES_PASSWORD", cfg.Database.Password},
	} {
		if required.value == "" {
			errs = append(errs, fmt.Errorf("%s is required", required.key))
		}
	}

	if !slices.Contains(validSSLModes, cfg.Database.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"invalid DB_SSLMODE %q: must be one of %v",
			cfg.Database.SSLMode, validSSLModes,
		))
	} else if cfg.Env == EnvProduction &&
		slices.Contains(insecureSSLModes, cfg.Database.SSLMode) {
		errs = append(errs, fmt.Errorf(
			"DB_SSLMODE %q is not allowed in production: "+
				"use require, verify-ca or verify-full",
			cfg.Database.SSLMode,
		))
	}

	maxConns, maxErr := parseInt32("DB_MAX_CONNS", get("DB_MAX_CONNS", "10"), 1)
	if maxErr != nil {
		errs = append(errs, maxErr)
	}
	minConns, minErr := parseInt32("DB_MIN_CONNS", get("DB_MIN_CONNS", "2"), 0)
	if minErr != nil {
		errs = append(errs, minErr)
	}
	if maxErr == nil && minErr == nil && minConns > maxConns {
		errs = append(errs, fmt.Errorf(
			"DB_MIN_CONNS (%d) must not exceed DB_MAX_CONNS (%d)",
			minConns, maxConns,
		))
	}
	cfg.Database.MaxConns = maxConns
	cfg.Database.MinConns = minConns

	if cfg.Database.StatementTimeout, err = parseDuration(
		"DB_STATEMENT_TIMEOUT", get("DB_STATEMENT_TIMEOUT", "30s"), true,
	); err != nil {
		errs = append(errs, err)
	}

	if err := errors.Join(errs...); err != nil {
		return nil, fmt.Errorf("invalid configuration:\n%w", err)
	}

	return cfg, nil
}

func parsePort(key, value string) (int, error) {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return 0, fmt.Errorf(
			"invalid %s %q: must be an integer between 1 and 65535", key, value,
		)
	}

	return port, nil
}

func parseDuration(key, value string, allowZero bool) (time.Duration, error) {
	d, err := time.ParseDuration(value)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		bound := "positive"
		if allowZero {
			bound = "non-negative"
		}
		return 0, fmt.Errorf(
			"invalid %s %q: must be a %s duration such as 10s", key, value, bound,
		)
	}

	return d, nil
}

func parseInt32(key, value string, minimum int32) (int32, error) {
	n, err := strconv.ParseInt(value, 10, 32)
	if err != nil || int32(n) < minimum {
		return 0, fmt.Errorf(
			"invalid %s %q: must be an integer >= %d", key, value, minimum,
		)
	}

	return int32(n), nil
}

// splitList splits a comma-separated value, trimming items and dropping
// empty ones.
func splitList(value string) []string {
	var items []string
	for _, item := range strings.Split(value, ",") {
		if item = strings.TrimSpace(item); item != "" {
			items = append(items, item)
		}
	}
	return items
}

// parseOrigins validates CORS_ALLOWED_ORIGINS and returns its canonical,
// de-duplicated origins, or nil when CORS is disabled. In production every
// origin must use https.
func parseOrigins(value string, production bool) ([]string, error) {
	var (
		origins []string
		errs    []error
	)
	for _, raw := range splitList(value) {
		origin, err := canonicalOrigin(raw)
		if err == nil && production && strings.HasPrefix(origin, "http://") {
			err = errors.New("must use https in production")
		}
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"invalid CORS_ALLOWED_ORIGINS entry %q: %w", raw, err,
			))
			continue
		}
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}

	return origins, errors.Join(errs...)
}

// canonicalOrigin returns raw as a browser would serialize it: a lowercase
// http or https scheme and host, with the scheme's default port removed and
// any other port kept. Paths, queries, fragments, credentials, wildcards,
// and the null origin are rejected.
func canonicalOrigin(raw string) (string, error) {
	if raw == "*" || strings.EqualFold(raw, "null") {
		return "", errors.New("wildcard and null origins are not allowed")
	}
	if strings.ContainsAny(raw, "?#*") {
		return "", errors.New("must be scheme://host[:port] only")
	}

	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("not a valid URL")
	}

	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("scheme must be http or https")
	}
	if u.Opaque != "" || u.User != nil || (u.Path != "" && u.Path != "/") {
		return "", errors.New("must be scheme://host[:port] only")
	}

	host := strings.ToLower(u.Hostname())
	if host == "" {
		return "", errors.New("host is required")
	}

	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", errors.New("port must be between 1 and 65535")
		}
		port = strconv.Itoa(n)
	}
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}

	if port != "" {
		return scheme + "://" + net.JoinHostPort(host, port), nil
	}
	if strings.Contains(host, ":") { // IPv6 literal
		return scheme + "://[" + host + "]", nil
	}
	return scheme + "://" + host, nil
}

// parseTrustedProxies validates TRUSTED_PROXIES and returns canonical CIDR
// strings, or nil when no proxy is trusted. Bare IPs become single-host
// networks and IPv4-mapped networks are written in IPv4 form, so Gin
// interprets every entry exactly as validated here.
//
// The list is rejected if its networks together cover every IPv4 or every
// IPv6 address, whether through one entry such as 0.0.0.0/0 or several
// such as 0.0.0.0/1,128.0.0.0/1: trusting every proxy would let any client
// choose its own IP through X-Forwarded-For.
func parseTrustedProxies(value string) ([]string, error) {
	var (
		proxies    []string
		errs       []error
		ipv4, ipv6 prefixNode
	)
	for _, raw := range splitList(value) {
		network, err := parseNetwork(raw)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"invalid TRUSTED_PROXIES entry %q: must be an IP address or CIDR", raw,
			))
			continue
		}

		addr, prefix, isIPv4 := networkFamily(network)
		bits := 128
		if isIPv4 {
			ipv4.insert(addr, prefix)
			bits = 32
		} else {
			ipv6.insert(addr, prefix)
		}
		proxies = append(proxies, (&net.IPNet{
			IP: addr, Mask: net.CIDRMask(prefix, bits),
		}).String())
	}

	if ipv4.covers() {
		errs = append(errs, errors.New(
			"TRUSTED_PROXIES covers all IPv4 addresses: trusting every proxy is not allowed",
		))
	}
	if ipv6.covers() {
		errs = append(errs, errors.New(
			"TRUSTED_PROXIES covers all IPv6 addresses: trusting every proxy is not allowed",
		))
	}

	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	return proxies, nil
}

// parseNetwork parses an IP address (as a single-host network) or a CIDR.
func parseNetwork(raw string) (*net.IPNet, error) {
	if strings.Contains(raw, "/") {
		_, network, err := net.ParseCIDR(raw)
		return network, err
	}

	ip := net.ParseIP(raw)
	if ip == nil {
		return nil, errors.New("invalid IP address")
	}
	if ip4 := ip.To4(); ip4 != nil {
		return &net.IPNet{IP: ip4, Mask: net.CIDRMask(32, 32)}, nil
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(128, 128)}, nil
}

// networkFamily classifies network the way net.IPNet.Contains matches it:
// IPv4 networks and IPv4-mapped IPv6 networks (/96 or longer) match IPv4
// clients and are returned as 4-byte IPv4 networks; all other networks are
// IPv6 and never match IPv4 clients.
func networkFamily(network *net.IPNet) (addr net.IP, prefix int, isIPv4 bool) {
	ones, bits := network.Mask.Size()
	if ip4 := network.IP.To4(); ip4 != nil {
		if bits == 128 {
			ones -= 96
		}
		return ip4, max(ones, 0), true
	}
	return network.IP.To16(), ones, false
}

// prefixNode is a node in a binary trie of address prefixes, used to decide
// whether a set of networks covers an entire address family. Inserting N
// prefixes of an address width W takes O(N·W) time and space, and covers
// visits each node once.
type prefixNode struct {
	child [2]*prefixNode
	// full means every address under this node is covered.
	full bool
}

// insert marks the first prefix bits of addr as covered.
func (n *prefixNode) insert(addr net.IP, prefix int) {
	node := n
	for i := range prefix {
		if node.full {
			return // already covered by a shorter prefix
		}
		bit := addr[i/8] >> (7 - i%8) & 1
		if node.child[bit] == nil {
			node.child[bit] = &prefixNode{}
		}
		node = node.child[bit]
	}
	node.full = true
	node.child = [2]*prefixNode{}
}

// covers reports whether every address under n is covered.
func (n *prefixNode) covers() bool {
	if n == nil {
		return false
	}
	if n.full {
		return true
	}
	return n.child[0].covers() && n.child[1].covers()
}
