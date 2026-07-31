package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ListenAddress  string
	GRPCTarget     string
	GRPCTLS        bool
	GRPCServerName string
	RequestTimeout time.Duration
	MaxBodyBytes   int64
}

func Load() (Config, error) {
	grpcTLS, err := envBool("SUI_GRPC_TLS", false)
	if err != nil {
		return Config{}, err
	}
	requestTimeout, err := envDuration("REQUEST_TIMEOUT", 10*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxBodyBytes, err := envInt64("MAX_BODY_BYTES", 1<<20)
	if err != nil {
		return Config{}, err
	}
	if maxBodyBytes <= 0 {
		return Config{}, fmt.Errorf("MAX_BODY_BYTES must be greater than zero")
	}

	target := strings.TrimSpace(os.Getenv("SUI_GRPC_TARGET"))
	if target == "" {
		return Config{}, fmt.Errorf("SUI_GRPC_TARGET is required")
	}

	return Config{
		ListenAddress:  envString("LISTEN_ADDRESS", ":8080"),
		GRPCTarget:     target,
		GRPCTLS:        grpcTLS,
		GRPCServerName: strings.TrimSpace(os.Getenv("SUI_GRPC_SERVER_NAME")),
		RequestTimeout: requestTimeout,
		MaxBodyBytes:   maxBodyBytes,
	}, nil
}

func envString(name, fallback string) string {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	return value
}

func envBool(name string, fallback bool) (bool, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}

func envDuration(name string, fallback time.Duration) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", name)
	}
	return parsed, nil
}

func envInt64(name string, fallback int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", name, err)
	}
	return parsed, nil
}
