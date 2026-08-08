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
}

func (f *fakeConn) Run(_ context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	f.commands = append(f.commands, cmd)
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		f.stdin = append(f.stdin, b...)
	}
	return f.out, f.err
}

func (f *fakeConn) Close() error { return nil }

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
