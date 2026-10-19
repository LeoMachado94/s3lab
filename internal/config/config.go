// Package config loads s3lab settings from flags and environment variables
// as documented in docs/configuration.md.
package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/LeoMachado94/s3lab/internal/storage"
)

const maxRequestLogSize = 100000

type Storage string

const (
	StorageFS     Storage = "fs"
	StorageMemory Storage = "memory"
)

// Config mirrors docs/configuration.md one field per setting.
type Config struct {
	Addr           string
	ConsoleAddr    string
	ConsoleEnabled bool
	ConsoleAuth    bool
	AccessKey      string
	SecretKey      string
	Region         string
	Anonymous      bool
	Storage        Storage
	DataDir        string
	Buckets        []string
	SeedDir        string
	// FaultsJSON is the raw S3LAB_FAULTS value.
	FaultsJSON  string
	Domain      string
	CORSOrigins []string
	// ConsoleAllowedHosts extends the Host allowlist of the console.
	ConsoleAllowedHosts []string
	MaxObjectSize       int64
	RequestLogSize      int
	LogLevel            string
	LogFormat           string
	// Warnings collected while loading (e.g. ignored MinIO arguments).
	Warnings []string
}

// Default returns the documented defaults for the binary (the image sets
// S3LAB_DATA_DIR=/data).
func Default() Config {
	return Config{
		Addr:           ":9000",
		ConsoleAddr:    ":9001",
		ConsoleEnabled: true,
		ConsoleAuth:    true,
		AccessKey:      "s3lab",
		SecretKey:      "s3labsecret",
		Region:         "us-east-1",
		Storage:        StorageFS,
		DataDir:        "./data",
		CORSOrigins:    []string{"*"},
		MaxObjectSize:  5 << 30,
		RequestLogSize: 1000,
		LogLevel:       "info",
		LogFormat:      "text",
	}
}

// Load parses args (without the program name and subcommand) on top of
// environment variables looked up via getenv. Flags win over env. An empty
// value counts as unset; use LoadWithLookup to tell them apart.
func Load(args []string, getenv func(string) string) (Config, error) {
	return LoadWithLookup(args, func(k string) (string, bool) {
		v := getenv(k)
		return v, v != ""
	})
}

type setting struct {
	env   string
	flag  string
	bool  bool
	apply func(c *Config, v string) error
}

func strSetting(env, flagName string, f func(c *Config) *string) setting {
	return setting{env: env, flag: flagName, apply: func(c *Config, v string) error {
		*f(c) = v
		return nil
	}}
}

func boolSetting(env, flagName string, f func(c *Config) *bool) setting {
	return setting{env: env, flag: flagName, bool: true, apply: func(c *Config, v string) error {
		b, err := ParseBool(v)
		if err != nil {
			return err
		}
		*f(c) = b
		return nil
	}}
}

func settings() []setting {
	return []setting{
		strSetting("S3LAB_ADDR", "addr", func(c *Config) *string { return &c.Addr }),
		strSetting("S3LAB_CONSOLE_ADDR", "console-addr", func(c *Config) *string { return &c.ConsoleAddr }),
		boolSetting("S3LAB_CONSOLE_ENABLED", "console", func(c *Config) *bool { return &c.ConsoleEnabled }),
		boolSetting("S3LAB_CONSOLE_AUTH", "console-auth", func(c *Config) *bool { return &c.ConsoleAuth }),
		strSetting("S3LAB_ACCESS_KEY", "access-key", func(c *Config) *string { return &c.AccessKey }),
		strSetting("S3LAB_SECRET_KEY", "secret-key", func(c *Config) *string { return &c.SecretKey }),
		strSetting("S3LAB_REGION", "region", func(c *Config) *string { return &c.Region }),
		boolSetting("S3LAB_ANONYMOUS", "anonymous", func(c *Config) *bool { return &c.Anonymous }),
		{env: "S3LAB_STORAGE", flag: "storage", apply: func(c *Config, v string) error {
			c.Storage = Storage(strings.ToLower(strings.TrimSpace(v)))
			return nil
		}},
		strSetting("S3LAB_DATA_DIR", "data-dir", func(c *Config) *string { return &c.DataDir }),
		{env: "S3LAB_BUCKETS", flag: "buckets", apply: func(c *Config, v string) error {
			c.Buckets = splitList(v)
			return nil
		}},
		strSetting("S3LAB_SEED_DIR", "seed-dir", func(c *Config) *string { return &c.SeedDir }),
		strSetting("S3LAB_FAULTS", "faults", func(c *Config) *string { return &c.FaultsJSON }),
		strSetting("S3LAB_DOMAIN", "domain", func(c *Config) *string { return &c.Domain }),
		{env: "S3LAB_CORS_ORIGINS", flag: "cors-origins", apply: func(c *Config, v string) error {
			c.CORSOrigins = splitList(v)
			return nil
		}},
		{env: "S3LAB_CONSOLE_ALLOWED_HOSTS", flag: "console-allowed-hosts", apply: func(c *Config, v string) error {
			c.ConsoleAllowedHosts = splitList(v)
			return nil
		}},
		{env: "S3LAB_MAX_OBJECT_SIZE", flag: "max-object-size", apply: func(c *Config, v string) error {
			n, err := ParseSize(v)
			c.MaxObjectSize = n
			return err
		}},
		{env: "S3LAB_REQUEST_LOG_SIZE", flag: "request-log-size", apply: func(c *Config, v string) error {
			n, err := strconv.Atoi(strings.TrimSpace(v))
			if err != nil {
				return fmt.Errorf("invalid integer %q", v)
			}
			c.RequestLogSize = n
			return nil
		}},
		{env: "S3LAB_LOG_LEVEL", flag: "log-level", apply: func(c *Config, v string) error {
			c.LogLevel = strings.ToLower(strings.TrimSpace(v))
			return nil
		}},
		{env: "S3LAB_LOG_FORMAT", flag: "log-format", apply: func(c *Config, v string) error {
			c.LogFormat = strings.ToLower(strings.TrimSpace(v))
			return nil
		}},
	}
}

var minioAliases = []struct {
	target  string
	sources []string
}{
	{"S3LAB_ACCESS_KEY", []string{"MINIO_ROOT_USER", "MINIO_ACCESS_KEY"}},
	{"S3LAB_SECRET_KEY", []string{"MINIO_ROOT_PASSWORD", "MINIO_SECRET_KEY"}},
	{"S3LAB_REGION", []string{"MINIO_REGION", "MINIO_SITE_REGION"}},
	{"S3LAB_DOMAIN", []string{"MINIO_DOMAIN"}},
}

// LoadWithLookup is Load with a lookup that distinguishes unset variables
// from empty ones (S3LAB_CORS_ORIGINS="" disables CORS).
func LoadWithLookup(args []string, lookup func(string) (string, bool)) (Config, error) {
	cfg := Default()
	table := settings()
	byEnv := map[string]setting{}
	known := map[string]setting{}
	for _, st := range table {
		byEnv[st.env] = st
		known[st.flag] = st
	}
	envSet := map[string]bool{}

	for _, st := range table {
		v, ok := lookup(st.env)
		if !ok {
			continue
		}
		envSet[st.env] = true
		if err := st.apply(&cfg, v); err != nil {
			return Config{}, fmt.Errorf("%s: %w", st.env, err)
		}
	}
	for _, al := range minioAliases {
		if envSet[al.target] {
			continue
		}
		for _, src := range al.sources {
			v, ok := lookup(src)
			if !ok {
				continue
			}
			if err := byEnv[al.target].apply(&cfg, v); err != nil {
				return Config{}, fmt.Errorf("%s: %w", src, err)
			}
			break
		}
	}

	flagArgs, positional, warnings := preScan(args, known)
	cfg.Warnings = append(cfg.Warnings, warnings...)

	fs := flag.NewFlagSet("s3lab", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flagSet := map[string]bool{}
	var applyErr error
	for _, st := range table {
		fn := func(v string) error {
			flagSet[st.flag] = true
			if err := st.apply(&cfg, v); err != nil {
				applyErr = fmt.Errorf("--%s: %w", st.flag, err)
				return applyErr
			}
			return nil
		}
		if st.bool {
			fs.BoolFunc(st.flag, st.env, fn)
		} else {
			fs.Func(st.flag, st.env, fn)
		}
	}
	if err := fs.Parse(flagArgs); err != nil {
		if applyErr != nil {
			return Config{}, applyErr
		}
		return Config{}, fmt.Errorf("flags: %w", err)
	}
	positional = append(positional, fs.Args()...)

	if len(positional) > 0 {
		if !envSet["S3LAB_DATA_DIR"] && !flagSet["data-dir"] {
			cfg.DataDir = positional[0]
			positional = positional[1:]
		}
		if len(positional) > 0 {
			cfg.Warnings = append(cfg.Warnings, "ignoring unexpected arguments: "+strings.Join(positional, " "))
		}
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

var minioIgnored = map[string]bool{"quiet": true, "json": true, "no-compat": true}

// minioValueFlags are MinIO flags that take a separate value token.
var minioValueFlags = map[string]bool{
	"certs-dir": true, "S": true, "config-dir": true, "C": true, "ftp": true, "sftp": true,
}

// preScan separates s3lab flags from MinIO-style arguments: it drops a
// leading "server", maps --address/--console-address and ignores unknown
// MinIO flags, collecting positionals (the data dir) wherever they appear.
func preScan(args []string, known map[string]setting) (flagArgs, positional, warnings []string) {
	minio := len(args) > 0 && args[0] == "server"
	if minio {
		args = args[1:]
		warnings = append(warnings, "ignoring MinIO arguments: `server` command; configure s3lab with S3LAB_* variables")
	}
	alias := map[string]string{"address": "addr", "console-address": "console-addr"}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			positional = append(positional, a)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(a, "-"), "=")
		if to, ok := alias[name]; ok {
			name = to
		}
		st, isKnown := known[name]
		switch {
		case isKnown:
			switch {
			case hasVal:
				flagArgs = append(flagArgs, "--"+name+"="+val)
			case st.bool:
				if i+1 < len(args) {
					if _, err := ParseBool(args[i+1]); err == nil {
						i++
						flagArgs = append(flagArgs, "--"+name+"="+args[i])
						break
					}
				}
				flagArgs = append(flagArgs, "--"+name)
			case i+1 < len(args):
				i++
				flagArgs = append(flagArgs, "--"+name+"="+args[i])
			default:
				flagArgs = append(flagArgs, "--"+name)
			}
		case minio || minioIgnored[name]:
			warnings = append(warnings, "ignoring MinIO flag --"+name)
			if minio && !hasVal && minioValueFlags[name] && i+1 < len(args) {
				i++
			}
		default:
			flagArgs = append(flagArgs, a)
		}
	}
	return flagArgs, positional, warnings
}

func splitList(v string) []string {
	out := []string{}
	for _, p := range strings.Split(v, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ParseBool accepts true/false/1/0/yes/no (case-insensitive).
func ParseBool(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	}
	return false, fmt.Errorf("invalid boolean %q (use true/false/1/0/yes/no)", v)
}

func (c *Config) validate() error {
	if len(c.Buckets) == 0 {
		c.Buckets = nil
	}
	switch c.Storage {
	case StorageFS, StorageMemory:
	default:
		return fmt.Errorf("S3LAB_STORAGE/--storage: invalid value %q (use fs or memory)", c.Storage)
	}
	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("S3LAB_LOG_LEVEL/--log-level: invalid value %q (use debug, info, warn or error)", c.LogLevel)
	}
	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("S3LAB_LOG_FORMAT/--log-format: invalid value %q (use text or json)", c.LogFormat)
	}
	if c.MaxObjectSize <= 0 {
		return errors.New("S3LAB_MAX_OBJECT_SIZE/--max-object-size: must be greater than zero")
	}
	if c.RequestLogSize <= 0 || c.RequestLogSize > maxRequestLogSize {
		return fmt.Errorf("S3LAB_REQUEST_LOG_SIZE/--request-log-size: must be between 1 and %d", maxRequestLogSize)
	}
	if !c.Anonymous && (c.AccessKey == "" || c.SecretKey == "") {
		return errors.New("S3LAB_ACCESS_KEY/S3LAB_SECRET_KEY: must not be empty unless anonymous access is enabled")
	}
	if strings.TrimSpace(c.Region) == "" {
		return errors.New("S3LAB_REGION/--region: must not be empty")
	}
	if strings.TrimSpace(c.Addr) == "" {
		return errors.New("S3LAB_ADDR/--addr: must not be empty")
	}
	if c.ConsoleEnabled && strings.TrimSpace(c.ConsoleAddr) == "" {
		return errors.New("S3LAB_CONSOLE_ADDR/--console-addr: must not be empty")
	}
	if c.ConsoleEnabled && c.ConsoleAuth && (c.AccessKey == "" || c.SecretKey == "") {
		return errors.New("S3LAB_CONSOLE_AUTH: console login needs S3LAB_ACCESS_KEY and S3LAB_SECRET_KEY (or set S3LAB_CONSOLE_AUTH=false)")
	}
	if c.Storage == StorageFS && strings.TrimSpace(c.DataDir) == "" {
		return errors.New("S3LAB_DATA_DIR/--data-dir: must not be empty with storage fs")
	}
	for _, b := range c.Buckets {
		if err := storage.ValidateBucketName(b); err != nil {
			return fmt.Errorf("S3LAB_BUCKETS/--buckets: %q: %w", b, err)
		}
	}
	if strings.TrimSpace(c.FaultsJSON) != "" {
		var rules []json.RawMessage
		if err := json.Unmarshal([]byte(c.FaultsJSON), &rules); err != nil {
			return fmt.Errorf("S3LAB_FAULTS/--faults: must be a JSON array: %w", err)
		}
	}
	return nil
}

var sizeUnits = map[string]int64{
	"":    1,
	"b":   1,
	"kib": 1 << 10,
	"mib": 1 << 20,
	"gib": 1 << 30,
	"tib": 1 << 40,
	"kb":  1000,
	"mb":  1000 * 1000,
	"gb":  1000 * 1000 * 1000,
	"tb":  1000 * 1000 * 1000 * 1000,
}

// ParseSize parses sizes such as "5GiB", "100MiB", "10KiB", "512B", "1024".
// KiB/MiB/GiB/TiB are 1024-based, KB/MB/GB/TB 1000-based; units are
// case-insensitive and a bare integer is bytes.
func ParseSize(s string) (int64, error) {
	t := strings.TrimSpace(s)
	i := 0
	for i < len(t) && t[i] >= '0' && t[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	n, err := strconv.ParseInt(t[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	mult, ok := sizeUnits[strings.ToLower(strings.TrimSpace(t[i:]))]
	if !ok {
		return 0, fmt.Errorf("invalid size unit in %q (use B, KiB, MiB, GiB, TiB, KB, MB, GB)", s)
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return n * mult, nil
}
