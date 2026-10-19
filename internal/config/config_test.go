package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func lookupFrom(env map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) {
		v, ok := env[k]
		return v, ok
	}
}

func TestParseSize(t *testing.T) {
	tests := []struct {
		in      string
		want    int64
		wantErr bool
	}{
		{"1024", 1024, false},
		{"512B", 512, false},
		{"10KiB", 10 << 10, false},
		{"100mib", 100 << 20, false},
		{"5GiB", 5 << 30, false},
		{"1TiB", 1 << 40, false},
		{"1KB", 1000, false},
		{"2MB", 2_000_000, false},
		{"3gb", 3_000_000_000, false},
		{" 7 MiB ", 7 << 20, false},
		{"", 0, true},
		{"abc", 0, true},
		{"5XiB", 0, true},
		{"-5", 0, true},
		{"99999999999TiB", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := ParseSize(tt.in)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := LoadWithLookup(nil, lookupFrom(nil))
	require.NoError(t, err)
	assert.Equal(t, Default(), cfg)
}

func TestLoadEnv(t *testing.T) {
	cfg, err := LoadWithLookup(nil, lookupFrom(map[string]string{
		"S3LAB_ADDR":             ":1",
		"S3LAB_CONSOLE_ADDR":     ":2",
		"S3LAB_CONSOLE_ENABLED":  "no",
		"S3LAB_ACCESS_KEY":       "ak",
		"S3LAB_SECRET_KEY":       "sk",
		"S3LAB_REGION":           "eu-west-1",
		"S3LAB_ANONYMOUS":        "YES",
		"S3LAB_STORAGE":          "memory",
		"S3LAB_DATA_DIR":         "/d",
		"S3LAB_BUCKETS":          " a-b , ,c-d",
		"S3LAB_SEED_DIR":         "/seed",
		"S3LAB_FAULTS":           `[{"x":1}]`,
		"S3LAB_DOMAIN":           "s3.local",
		"S3LAB_CORS_ORIGINS":     "http://a.com, http://b.com",
		"S3LAB_MAX_OBJECT_SIZE":  "10MiB",
		"S3LAB_REQUEST_LOG_SIZE": "5",
		"S3LAB_LOG_LEVEL":        "DEBUG",
		"S3LAB_LOG_FORMAT":       "json",
	}))
	require.NoError(t, err)
	assert.Equal(t, Config{
		Addr: ":1", ConsoleAddr: ":2", ConsoleEnabled: false, AccessKey: "ak", SecretKey: "sk",
		Region: "eu-west-1", Anonymous: true, Storage: StorageMemory, DataDir: "/d",
		Buckets: []string{"a-b", "c-d"}, SeedDir: "/seed", FaultsJSON: `[{"x":1}]`, Domain: "s3.local",
		CORSOrigins: []string{"http://a.com", "http://b.com"}, MaxObjectSize: 10 << 20,
		RequestLogSize: 5, LogLevel: "debug", LogFormat: "json",
	}, cfg)
}

func TestLoadCORS(t *testing.T) {
	cfg, err := LoadWithLookup(nil, lookupFrom(map[string]string{"S3LAB_CORS_ORIGINS": ""}))
	require.NoError(t, err)
	assert.NotNil(t, cfg.CORSOrigins)
	assert.Empty(t, cfg.CORSOrigins)

	cfg, err = Load(nil, func(string) string { return "" })
	require.NoError(t, err)
	assert.Equal(t, []string{"*"}, cfg.CORSOrigins)

	cfg, err = LoadWithLookup([]string{"--cors-origins="}, lookupFrom(nil))
	require.NoError(t, err)
	assert.Empty(t, cfg.CORSOrigins)
}

func TestMinioAliases(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want [4]string
	}{
		{"root vars", map[string]string{"MINIO_ROOT_USER": "u", "MINIO_ROOT_PASSWORD": "p", "MINIO_REGION": "r", "MINIO_DOMAIN": "d.io"}, [4]string{"u", "p", "r", "d.io"}},
		{"legacy vars", map[string]string{"MINIO_ACCESS_KEY": "u", "MINIO_SECRET_KEY": "p", "MINIO_SITE_REGION": "r"}, [4]string{"u", "p", "r", ""}},
		{"root wins over legacy", map[string]string{"MINIO_ROOT_USER": "u1", "MINIO_ACCESS_KEY": "u2"}, [4]string{"u1", "s3labsecret", "us-east-1", ""}},
		{"s3lab wins", map[string]string{"S3LAB_ACCESS_KEY": "mine", "MINIO_ROOT_USER": "u", "S3LAB_REGION": "x", "MINIO_REGION": "r"}, [4]string{"mine", "s3labsecret", "x", ""}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadWithLookup(nil, lookupFrom(tt.env))
			require.NoError(t, err)
			assert.Equal(t, tt.want, [4]string{cfg.AccessKey, cfg.SecretKey, cfg.Region, cfg.Domain})
		})
	}
}

func TestFlagsWinOverEnv(t *testing.T) {
	cfg, err := LoadWithLookup([]string{
		"--addr", ":7", "--console-addr=:8", "--console=false", "--anonymous", "--storage", "memory",
		"--buckets", "x-1,y-2", "--max-object-size", "1KiB", "--log-level=warn", "--request-log-size", "3",
	}, lookupFrom(map[string]string{"S3LAB_ADDR": ":1", "S3LAB_STORAGE": "fs", "S3LAB_LOG_LEVEL": "debug"}))
	require.NoError(t, err)
	assert.Equal(t, ":7", cfg.Addr)
	assert.Equal(t, ":8", cfg.ConsoleAddr)
	assert.False(t, cfg.ConsoleEnabled)
	assert.True(t, cfg.Anonymous)
	assert.Equal(t, StorageMemory, cfg.Storage)
	assert.Equal(t, []string{"x-1", "y-2"}, cfg.Buckets)
	assert.EqualValues(t, 1024, cfg.MaxObjectSize)
	assert.Equal(t, "warn", cfg.LogLevel)
	assert.Equal(t, 3, cfg.RequestLogSize)
}

func TestValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		args []string
		env  map[string]string
		want string
	}{
		{"storage", nil, map[string]string{"S3LAB_STORAGE": "s3"}, "S3LAB_STORAGE"},
		{"log level", []string{"--log-level", "loud"}, nil, "log-level"},
		{"log format", nil, map[string]string{"S3LAB_LOG_FORMAT": "xml"}, "S3LAB_LOG_FORMAT"},
		{"size", nil, map[string]string{"S3LAB_MAX_OBJECT_SIZE": "lots"}, "S3LAB_MAX_OBJECT_SIZE"},
		{"size flag", []string{"--max-object-size=1ZB"}, nil, "--max-object-size"},
		{"log size zero", nil, map[string]string{"S3LAB_REQUEST_LOG_SIZE": "0"}, "request-log-size"},
		{"log size nan", []string{"--request-log-size=x"}, nil, "--request-log-size"},
		{"bool", nil, map[string]string{"S3LAB_ANONYMOUS": "maybe"}, "S3LAB_ANONYMOUS"},
		{"bool flag", []string{"--console=maybe"}, nil, "--console"},
		{"empty addr", nil, map[string]string{"S3LAB_ADDR": " "}, "S3LAB_ADDR"},
		{"empty console addr", []string{"--console-addr", " "}, nil, "console-addr"},
		{"bucket", nil, map[string]string{"S3LAB_BUCKETS": "ok-bucket,Bad_Name"}, "S3LAB_BUCKETS"},
		{"faults not json", nil, map[string]string{"S3LAB_FAULTS": "{"}, "S3LAB_FAULTS"},
		{"faults not array", nil, map[string]string{"S3LAB_FAULTS": `{"a":1}`}, "S3LAB_FAULTS"},
		{"unknown flag", []string{"--nope"}, nil, "nope"},
		{"empty access key", []string{"--access-key="}, nil, "S3LAB_ACCESS_KEY"},
		{"empty secret key", []string{"--secret-key="}, nil, "S3LAB_SECRET_KEY"},
		{"empty region", []string{"--region", " "}, nil, "region"},
		{"log size too big", []string{"--request-log-size=100001"}, nil, "request-log-size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := LoadWithLookup(tt.args, lookupFrom(tt.env))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.want)
		})
	}
}

func TestMinioServerArgs(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		env      map[string]string
		dataDir  string
		console  string
		addr     string
		warnings bool
	}{
		{"data dir and console address", []string{"server", "/data", "--console-address", ":9001"}, nil, "/data", ":9001", ":9000", true},
		{"equals form and address", []string{"server", "/mnt", "--address=:9100", "--console-address=:9101"}, nil, "/mnt", ":9101", ":9100", true},
		{"flags before positional", []string{"server", "--console-address", ":9002", "/srv"}, nil, "/srv", ":9002", ":9000", true},
		{"unknown flags ignored", []string{"server", "/d", "--quiet", "--json"}, nil, "/d", ":9001", ":9000", true},
		{"env data dir wins", []string{"server", "/arg"}, map[string]string{"S3LAB_DATA_DIR": "/env"}, "/env", ":9001", ":9000", true},
		{"flag data dir wins", []string{"server", "/arg", "--data-dir", "/flag"}, nil, "/flag", ":9001", ":9000", true},
		{"server alone", []string{"server"}, nil, "./data", ":9001", ":9000", true},
		{"s3lab flags still work", []string{"server", "/d", "--storage=memory"}, nil, "/d", ":9001", ":9000", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadWithLookup(tt.args, lookupFrom(tt.env))
			require.NoError(t, err)
			assert.Equal(t, tt.dataDir, cfg.DataDir)
			assert.Equal(t, tt.console, cfg.ConsoleAddr)
			assert.Equal(t, tt.addr, cfg.Addr)
			assert.Equal(t, tt.warnings, len(cfg.Warnings) > 0)
		})
	}
}

func TestMinioQuietWithoutServer(t *testing.T) {
	cfg, err := LoadWithLookup([]string{"--quiet", "--storage=memory"}, lookupFrom(nil))
	require.NoError(t, err)
	assert.Equal(t, StorageMemory, cfg.Storage)
	assert.NotEmpty(t, cfg.Warnings)
}

func TestExtraPositionalWarns(t *testing.T) {
	cfg, err := LoadWithLookup([]string{"server", "/a", "/b"}, lookupFrom(nil))
	require.NoError(t, err)
	assert.Equal(t, "/a", cfg.DataDir)
	assert.Len(t, cfg.Warnings, 2)
}

func TestAnonymousAllowsEmptyKeys(t *testing.T) {
	cfg, err := LoadWithLookup([]string{"--anonymous", "--access-key=", "--secret-key="}, lookupFrom(nil))
	require.NoError(t, err)
	assert.True(t, cfg.Anonymous)
}

func TestLogSizeCap(t *testing.T) {
	cfg, err := LoadWithLookup([]string{"--request-log-size=100000"}, lookupFrom(nil))
	require.NoError(t, err)
	assert.Equal(t, 100000, cfg.RequestLogSize)
}

func TestConsoleAllowedHosts(t *testing.T) {
	cfg, err := LoadWithLookup(nil, lookupFrom(map[string]string{"S3LAB_CONSOLE_ALLOWED_HOSTS": "a.test, b.test"}))
	require.NoError(t, err)
	assert.Equal(t, []string{"a.test", "b.test"}, cfg.ConsoleAllowedHosts)

	cfg, err = LoadWithLookup([]string{"--console-allowed-hosts", "c.test"}, lookupFrom(nil))
	require.NoError(t, err)
	assert.Equal(t, []string{"c.test"}, cfg.ConsoleAllowedHosts)
}

func TestArgScanning(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		dataDir string
		addr    string
		console bool
		anon    bool
		warn    bool
	}{
		{"bool separate false", []string{"--console", "false", "/d"}, "/d", ":9000", false, false, false},
		{"bool separate true", []string{"--anonymous", "true", "/d"}, "/d", ":9000", true, true, false},
		{"bool no value keeps positional", []string{"--anonymous", "/d"}, "/d", ":9000", true, true, false},
		{"positional before flags", []string{"/data", "--addr", ":9100"}, "/data", ":9100", true, false, false},
		{"positional between flags", []string{"--anonymous", "/data", "--addr=:9100"}, "/data", ":9100", true, true, false},
		{"minio value flags consumed", []string{"server", "--certs-dir", "/certs", "/data", "-S", "/s", "--config-dir", "/c", "-C", "/cc", "--ftp", "x", "--sftp", "y"}, "/data", ":9000", true, false, true},
		{"minio positional before flags", []string{"server", "/data", "--address", ":9100"}, "/data", ":9100", true, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := LoadWithLookup(tt.args, lookupFrom(nil))
			require.NoError(t, err)
			assert.Equal(t, tt.dataDir, cfg.DataDir)
			assert.Equal(t, tt.addr, cfg.Addr)
			assert.Equal(t, tt.console, cfg.ConsoleEnabled)
			assert.Equal(t, tt.anon, cfg.Anonymous)
			assert.Equal(t, tt.warn, len(cfg.Warnings) > 0)
		})
	}
}
