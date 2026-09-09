package persistence

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileNetworkStoreRejectsCorruptAndOversizedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "malformed json", data: []byte("{not-json"), want: ErrNetworkStoreCorrupt},
		{name: "unsupported version", data: []byte(`{"version":2,"next_id":0,"networks":[],"ports":[],"endpoints":[]}`), want: ErrNetworkStoreCorrupt},
		{name: "oversized snapshot", data: []byte(strings.Repeat("x", MaxNetworkStoreFileBytes+1)), want: ErrNetworkStoreTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "networks.json")
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := NewFileNetworkStore(path); !errors.Is(err, tt.want) {
				t.Fatalf("NewFileNetworkStore() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFileNetworkStoreRejectsCrossScopeRelationships(t *testing.T) {
	store, err := NewFileNetworkStore(filepath.Join(t.TempDir(), "networks.json"))
	if err != nil {
		t.Fatalf("NewFileNetworkStore() error = %v", err)
	}
	ctx := context.Background()
	network, err := store.CreateNetwork(ctx, "scope-a", "frontend")
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	if _, err := store.CreateNetwork(ctx, "scope-a", "frontend"); !errors.Is(err, ErrDuplicateNetwork) {
		t.Fatalf("duplicate CreateNetwork() error = %v, want ErrDuplicateNetwork", err)
	}
	if _, err := store.AllocatePort(ctx, "scope-b", network.ID, "workload", 8080, models.NetworkProtocolTCP); !errors.Is(err, ErrNetworkNotFound) {
		t.Fatalf("cross-scope AllocatePort() error = %v, want ErrNetworkNotFound", err)
	}
	if _, err := store.GetNetwork(ctx, "scope-b", network.ID); !errors.Is(err, ErrNetworkNotFound) {
		t.Fatalf("cross-scope GetNetwork() error = %v, want ErrNetworkNotFound", err)
	}
}
