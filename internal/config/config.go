package config

import (
	"os"
	"strconv"
)

type Config struct {
	Port                    string
	GRPCPort                string
	DatabasePath            string
	AutoCreateQueues        bool
	DefaultProject          string
	DefaultLocation         string
	WorkerConcurrency       int
	WorkerPollIntervalMs    int
	InitialBackoffSeconds   int
	MaxBackoffSeconds       int
	DefaultMaxRetries       int
	TaskDispatchTimeoutSecs int
}

func Load() (*Config, error) {
	cfg := &Config{
		Port:                    getEnv("PORT", "8085"),
		GRPCPort:                getEnv("GRPC_PORT", "9090"),
		DatabasePath:            getEnv("DATABASE_PATH", "./tasks.db"),
		AutoCreateQueues:        getEnvBool("AUTO_CREATE_QUEUES", true),
		DefaultProject:          getEnv("DEFAULT_PROJECT", "local-project"),
		DefaultLocation:         getEnv("DEFAULT_LOCATION", "us-central1"),
		WorkerConcurrency:       getEnvInt("WORKER_CONCURRENCY", 10),
		WorkerPollIntervalMs:    getEnvInt("WORKER_POLL_INTERVAL_MS", 500),
		InitialBackoffSeconds:   getEnvInt("INITIAL_BACKOFF_SECONDS", 1),
		MaxBackoffSeconds:       getEnvInt("MAX_BACKOFF_SECONDS", 60),
		DefaultMaxRetries:       getEnvInt("DEFAULT_MAX_RETRIES", 5),
		TaskDispatchTimeoutSecs: getEnvInt("TASK_DISPATCH_TIMEOUT_SECS", 30),
	}
	return cfg, nil
}

func getEnv(key, defaultVal string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return defaultVal
}

func getEnvBool(key string, defaultVal bool) bool {
	if v := os.Getenv(key); v != "" {
		b, _ := strconv.ParseBool(v)
		return b
	}
	return defaultVal
}

func getEnvInt(key string, defaultVal int) int {
	if v := os.Getenv(key); v != "" {
		i, err := strconv.Atoi(v)
		if err == nil {
			return i
		}
	}
	return defaultVal
}
