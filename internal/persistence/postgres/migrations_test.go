package postgres

import (
	"strings"
	"testing"
)

func TestMigrationsEmbedPhase2DeclarativeState(t *testing.T) {
	migrations := Migrations()
	if len(migrations) != 2 {
		t.Fatalf("embedded migrations=%d, want 2", len(migrations))
	}
	if !strings.Contains(migrations[1], "CREATE TABLE IF NOT EXISTS declarative_states") {
		t.Fatalf("embedded Phase 2 migration does not define declarative state: %q", migrations[1])
	}
}
