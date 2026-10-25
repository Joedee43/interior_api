package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newStore(t *testing.T) *LocalStore {
	t.Helper()
	s, err := NewLocalStore(t.TempDir()) // removed automatically after the test
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPutOpenDelete(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()

	n, err := s.Put(ctx, "media/abc-123", strings.NewReader("hello"))
	if err != nil || n != 5 {
		t.Fatalf("Put = %d, %v", n, err)
	}
	f, err := s.Open(ctx, "media/abc-123")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(f)
	f.Close()
	if string(b) != "hello" {
		t.Errorf("content = %q", b)
	}

	if err := s.Delete(ctx, "media/abc-123"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(ctx, "media/abc-123"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open after delete: %v, want ErrNotFound", err)
	}
	if err := s.Delete(ctx, "media/abc-123"); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
}

func TestRejectsUnsafeKeys(t *testing.T) {
	s := newStore(t)
	for _, key := range []string{
		"../escape", "media/../../escape", "/abs", `media\win`, "C:/x", "", "media/", "Media/Upper", "a/b/c/d/e",
	} {
		if _, err := s.Put(context.Background(), key, strings.NewReader("x")); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("Put(%q) = %v, want ErrInvalidKey", key, err)
		}
	}
}

// failingReader returns some data and then an error, like a client that
// disconnects mid-upload.
type failingReader struct{ sent bool }

func (r *failingReader) Read(p []byte) (int, error) {
	if !r.sent {
		r.sent = true
		return copy(p, "partial"), nil
	}
	return 0, errors.New("connection reset")
}

func TestFailedPutLeavesNothing(t *testing.T) {
	s := newStore(t)
	if _, err := s.Put(context.Background(), "media/broken", &failingReader{}); err == nil {
		t.Fatal("expected error")
	}
	if _, err := s.Open(context.Background(), "media/broken"); !errors.Is(err, ErrNotFound) {
		t.Errorf("partial file visible: %v", err)
	}
	entries, _ := os.ReadDir(filepath.Join(s.root, "media"))
	if len(entries) != 0 {
		t.Errorf("leftover temp files: %v", entries)
	}
}

func TestCancelledContextStopsPut(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Put(ctx, "media/cancelled", strings.NewReader("data")); !errors.Is(err, context.Canceled) {
		t.Errorf("Put with cancelled ctx = %v", err)
	}
}
