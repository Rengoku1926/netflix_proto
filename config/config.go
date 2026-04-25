package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server ServerConfig
	Breakers BreakersConfig
}

type ServerConfig struct {
	Port string
	ReadTimeout time.Duration
	WriteTimeout time.Duration
	IdleTimeout time.Duration
}

type BreakersConfig struct {
	Payment BreakerConfig
	Recommendation BreakerConfig
	User BreakerConfig
}

type BreakerConfig struct {
	FailureThreshold int // number of consecutive failures before opening the circuit
	SuccessThreshold int // number of consecutive successes required to close the circuit from half-open state
	OpenTimeout time.Duration // how long to stay open before allowing a probe
	MaxHalfOpenProbes int //max concurrent probes when in half-open state
}

func Load() Config {
    return Config{
        Server: ServerConfig{
            Port:         getEnv("PORT", "8080"),
            ReadTimeout:  getDuration("READ_TIMEOUT", 10*time.Second),
            WriteTimeout: getDuration("WRITE_TIMEOUT", 30*time.Second),
            IdleTimeout:  getDuration("IDLE_TIMEOUT", 120*time.Second),
        },
        Breakers: BreakersConfig{
            Payment: BreakerConfig{
                FailureThreshold:  getInt("PAYMENT_FAILURE_THRESHOLD", 3),
                SuccessThreshold:  getInt("PAYMENT_SUCCESS_THRESHOLD", 2),
                OpenTimeout:       getDuration("PAYMENT_OPEN_TIMEOUT", 5*time.Second),
                MaxHalfOpenProbes: getInt("PAYMENT_MAX_PROBES", 1),
            },
            Recommendation: BreakerConfig{
                FailureThreshold:  getInt("RECO_FAILURE_THRESHOLD", 5),
                SuccessThreshold:  getInt("RECO_SUCCESS_THRESHOLD", 2),
                OpenTimeout:       getDuration("RECO_OPEN_TIMEOUT", 8*time.Second),
                MaxHalfOpenProbes: getInt("RECO_MAX_PROBES", 2),
            },
            User: BreakerConfig{
                FailureThreshold:  getInt("USER_FAILURE_THRESHOLD", 3),
                SuccessThreshold:  getInt("USER_SUCCESS_THRESHOLD", 1),
                OpenTimeout:       getDuration("USER_OPEN_TIMEOUT", 6*time.Second),
                MaxHalfOpenProbes: getInt("USER_MAX_PROBES", 1),
            },
        },
    }
}

func getEnv(key, def string) string {
    if v := os.Getenv(key); v != "" {
        return v
    }
    return def
}

func getInt(key string, def int) int {
    if v := os.Getenv(key); v != "" {
        if n, err := strconv.Atoi(v); err == nil {
            return n
        }
    }
    return def
}

func getDuration(key string, def time.Duration) time.Duration {
    if v := os.Getenv(key); v != "" {
        if d, err := time.ParseDuration(v); err == nil {
            return d
        }
    }
    return def
}