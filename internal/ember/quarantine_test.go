package ember

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveToQuarantineMovesOnlyConfinedProviderFiles(t *testing.T) {
	rootPath := t.TempDir()
	fileStore, err := NewFileStore(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(rootPath, "staging", "orphan.part")
	if err := os.WriteFile(sourcePath, []byte("uncertain bytes"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := fileStore.MoveToQuarantine("staging/orphan.part"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sourcePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	quarantineEntries, err := os.ReadDir(filepath.Join(rootPath, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(quarantineEntries) != 1 {
		t.Fatalf("quarantine entries=%d, want 1", len(quarantineEntries))
	}
	quarantinedBytes, err := os.ReadFile(filepath.Join(rootPath, "quarantine", quarantineEntries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(quarantinedBytes) != "uncertain bytes" {
		t.Fatalf("quarantined bytes=%q", quarantinedBytes)
	}
}

func TestMoveToQuarantineRejectsTraversalAndSymlinkEscape(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := t.TempDir()
	fileStore, err := NewFileStore(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outsidePath, "secret"), []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outsidePath, "secret"), filepath.Join(rootPath, "staging", "escape.part")); err != nil {
		t.Fatal(err)
	}

	for _, storedReference := range []string{"../outside", "/etc/passwd", "quarantine/already.quarantine", "staging/escape.part"} {
		if err := fileStore.MoveToQuarantine(storedReference); !errors.Is(err, ErrPathUnsafe) {
			t.Fatalf("reference %q error=%v, want ErrPathUnsafe", storedReference, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outsidePath, "secret")); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
}
