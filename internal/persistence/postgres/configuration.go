package postgres

import "fmt"

const RequiredMajor = 16
const SchemaComponent = "phase1"
const SchemaVersion = 1

type Config struct {
	MaxConnections       int
	ConnectTimeout       string
	StatementTimeout     string
	IdleTxTimeout        string
	RequiredMajorVersion int
}

func DefaultConfig() Config {
	return Config{
		MaxConnections:       10,
		ConnectTimeout:       "5s",
		StatementTimeout:     "15s",
		IdleTxTimeout:        "30s",
		RequiredMajorVersion: RequiredMajor,
	}
}

func (config Config) Validate() error {
	if config.MaxConnections != 10 || config.ConnectTimeout != "5s" || config.StatementTimeout != "15s" || config.IdleTxTimeout != "30s" || config.RequiredMajorVersion != RequiredMajor {
		return fmt.Errorf("incompatible PostgreSQL Phase 1 configuration")
	}
	return nil
}
