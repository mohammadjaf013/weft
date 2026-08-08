package local

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

func newTest(t *testing.T) *Storage {
	t.Helper()
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func ref(name, uri string) core.AssetRef { return core.AssetRef{Name: name, URI: uri} }

func TestSaveOpenRoundTrip(t *testing.T) {
	s := newTest(t)
	ctx := context.Background()
	r := ref("a.txt", "local:dir/a.txt")
	payload := "hello weft"
	if err := s.Save(ctx, r, strings.NewReader(payload)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	in, err := s.Open(ctx, r)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer in.Close()
	b, _ := io.ReadAll(in)
	if string(b) != payload {
		t.Errorf("round trip = %q, want %q", string(b), payload)
	}
}

func TestCopyAndDelete(t *testing.T) {
	s := newTest(t)
	ctx := context.Background()
	src := ref("s", "local:src.bin")
	dst := ref("d", "local:dst.bin")
	if err := s.Save(ctx, src, strings.NewReader("data")); err != nil {
		t.Fatal(err)
	}
	if err := s.Copy(ctx, src, dst); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	in, err := s.Open(ctx, dst)
	if err != nil {
		t.Fatalf("Open dst: %v", err)
	}
	b, _ := io.ReadAll(in)
	in.Close()
	if string(b) != "data" {
		t.Errorf("copied = %q", string(b))
	}
	if err := s.Delete(ctx, dst); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Open(ctx, dst); err == nil {
		t.Error("expected Open after Delete to fail")
	}
}

func TestVerifyMissing(t *testing.T) {
	s := newTest(t)
	ok, err := s.Verify(context.Background(), ref("m", "local:missing.bin"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if ok {
		t.Error("Verify returned true for missing file")
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s := newTest(t)
	ctx := context.Background()
	evil := ref("e", "local:../escape.bin")
	if err := s.Save(ctx, evil, bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected path traversal to be rejected")
	}
	if _, err := s.Open(ctx, evil); err == nil {
		t.Fatal("expected Open of traversal path to fail")
	}
}
