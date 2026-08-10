package ssh

import (
	"context"
	"io"
	"strings"
	"testing"

	"github.com/mohammadjaf013/weft/core"
)

type fakeConn struct {
	commands []string
	out      []byte
	stdin    []byte
	err      error
	closed   int
}

func (f *fakeConn) Run(_ context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	f.commands = append(f.commands, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, b...)
	}
	return f.out, f.err
}

func (f *fakeConn) Close() error { f.closed++; return nil }

func newTest(t *testing.T, cfg Config) (*Storage, *fakeConn) {
	t.Helper()
	fc := &fakeConn{out: []byte("remote-content")}
	cfg.Host = "10.0.0.1"
	cfg.User = "weft"
	cfg.Conn = fc
	s, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s, fc
}

func TestOpenCatsRemote(t *testing.T) {
	s, fc := newTest(t, Config{KeyPath: "/k"})
	in, err := s.Open(context.Background(), core.AssetRef{Name: "v.mp4", URI: "ssh:v.mp4"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	b, _ := io.ReadAll(in)
	in.Close()
	if string(b) != "remote-content" {
		t.Errorf("got %q", string(b))
	}
	if !strings.Contains(fc.commands[0], "cat") {
		t.Errorf("expected cat: %v", fc.commands)
	}
}

func TestSaveStreamsViaStdin(t *testing.T) {
	s, fc := newTest(t, Config{KeyPath: "/k"})
	payload := "payload-bytes"
	err := s.Save(context.Background(), core.AssetRef{Name: "o.mp4", URI: "ssh:o.mp4"}, strings.NewReader(payload))
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if string(fc.stdin) != payload {
		t.Errorf("stdin = %q, want %q", string(fc.stdin), payload)
	}
	joined := strings.Join(fc.commands, " | ")
	if !strings.Contains(joined, "mkdir -p") || !strings.Contains(joined, "cat >") {
		t.Errorf("expected mkdir+cat commands: %v", fc.commands)
	}
}

func TestDeleteRunsRm(t *testing.T) {
	s, fc := newTest(t, Config{KeyPath: "/k"})
	if err := s.Delete(context.Background(), core.AssetRef{Name: "o.mp4", URI: "ssh:o.mp4"}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !strings.Contains(fc.commands[0], "rm -f") {
		t.Errorf("expected rm -f: %v", fc.commands)
	}
}

func TestCopyRunsCp(t *testing.T) {
	s, fc := newTest(t, Config{KeyPath: "/k"})
	if err := s.Copy(context.Background(),
		core.AssetRef{Name: "a", URI: "ssh:src.mp4"},
		core.AssetRef{Name: "b", URI: "ssh:dst.mp4"}); err != nil {
		t.Fatalf("Copy: %v", err)
	}
	if !strings.Contains(fc.commands[0], "cp ") {
		t.Errorf("expected cp: %v", fc.commands)
	}
}

// TestMultipleOpsDoNotCloseTheConnectionBetweenCalls guards against
// reintroducing a dial+close per file: a job uploading a full HLS ladder
// makes dozens of Save calls, and each used to pay a full TCP+SSH handshake
// because the old code closed the connection at the end of every method.
func TestMultipleOpsDoNotCloseTheConnectionBetweenCalls(t *testing.T) {
	s, fc := newTest(t, Config{KeyPath: "/k"})
	for i := 0; i < 5; i++ {
		if err := s.Save(context.Background(), core.AssetRef{Name: "seg.ts", URI: "ssh:seg.ts"}, strings.NewReader("x")); err != nil {
			t.Fatalf("Save %d: %v", i, err)
		}
	}
	if fc.closed != 0 {
		t.Errorf("connection closed %d times mid-job; Save must not close the shared connection", fc.closed)
	}
}

// TestConnectCachesAndCloseReleases exercises the caching path directly
// (bypassing cfg.Conn, which always short-circuits to the injected fake) by
// seeding the unexported cache field, since a real dial isn't unit-testable
// without a listening SSH server.
func TestConnectCachesAndCloseReleases(t *testing.T) {
	s, err := New(Config{Host: "10.0.0.1", User: "weft", KeyPath: "/k"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fc := &fakeConn{out: []byte("cached")}
	s.conn = fc

	got, err := s.connect(context.Background())
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if got != Conn(fc) {
		t.Fatal("connect() should return the cached connection instead of dialing")
	}

	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if fc.closed != 1 {
		t.Errorf("Close() should close the cached connection exactly once, got %d", fc.closed)
	}
	if s.conn != nil {
		t.Error("Close() should clear the cached connection")
	}
	// Close is safe to call again (no cached connection left).
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

func TestNewRequiresCredentialAndHost(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("expected error for empty config")
	}
	if _, err := New(Config{Host: "h", User: "u"}); err == nil {
		t.Fatal("expected error without key or password")
	}
	if _, err := New(Config{Host: "h", User: "u", Password: "pw"}); err != nil {
		t.Fatalf("password-only config must be valid: %v", err)
	}
	if _, err := New(Config{Host: "h", User: "u", KeyPath: "/k"}); err != nil {
		t.Fatalf("key-only config must be valid: %v", err)
	}
}
