// Package config loads s3lab settings from flags and environment variables
// as documented in docs/configuration.md. Owner: task T3.A.
package config

import (
	"errors"
	"time"
)

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
	AccessKey      string
	SecretKey      string
	Region         string
	Anonymous      bool
	Storage        Storage
	DataDir        string
	Buckets        []string
	SeedDir        string
	// FaultsJSON is the raw S3LAB_FAULTS value.
	FaultsJSON     string
	Domain         string
	CORSOrigins    []string
	MaxObjectSize  int64
	RequestLogSize int
	LogLevel       string
	LogFormat      string
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
// environment variables looked up via getenv. Flags win over env.
func Load(args []string, getenv func(string) string) (Config, error) {
	return Config{}, errors.New("config: not implemented")
}

// ParseSize parses sizes such as "5GiB", "100MiB", "10KiB", "512B", "1024".
func ParseSize(s string) (int64, error) {
	return 0, errors.New("config: not implemented")
}

var _ = time.Second
