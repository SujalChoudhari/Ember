package ember

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestMoveToQuarantineMovesOnlyConfinedProviderFiles(t *testing.T) {
	root := t.TempDir()
	files, err := NewFileStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "staging", "orphan.part")
	if err := os.WriteFile(source, []byte("uncertain bytes"), 0o640); err != nil {
		t.Fatal(err)
	}

	if err := files.MoveToQuarantine("staging/orphan.part"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("source still exists: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("quarantine entries=%d, want 1", len(entries))
	}
	got, err := os.ReadFile(filepath.Join(root, "quarantine", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "uncertain bytes" {
		t.Fatalf("quarantined bytes=%q", got)
	}
}

func TestMoveToQuarantineRejectsTraversalAndSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	files, err := NewFileStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, "secret"), []byte("secret"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret"), filepath.Join(root, "staging", "escape.part")); err != nil {
		t.Fatal(err)
	}

	for _, reference := range []string{"../outside", "/etc/passwd", "quarantine/already.quarantine", "staging/escape.part"} {
		if err := files.MoveToQuarantine(reference); !errors.Is(err, ErrPathUnsafe) {
			t.Fatalf("reference %q error=%v, want ErrPathUnsafe", reference, err)
		}
	}
	if _, err := os.Stat(filepath.Join(outside, "secret")); err != nil {
		t.Fatalf("outside file changed: %v", err)
	}
}
