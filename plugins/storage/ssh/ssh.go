// Package ssh implements core.Storage over an SSH destination server using
// golang.org/x/crypto/ssh — a native client, no shelling out to ssh/scp
// binaries. Authentication is either a private key (KeyPath) or a password,
// so a server can be registered with user+password just as easily as with a
// key. Commands run over a single session; uploads stream via stdin.
package ssh

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
	"github.com/mohammadjaf013/weft/core"
)

type Config struct {
	Host     string
	User     string
	KeyPath  string // path to the private key; used when Password is empty
	Password string // password auth; used when KeyPath is empty
	BasePath string // remote base dir, e.g. /srv/weft
	Port     int
	Timeout  time.Duration
	// Conn is overridable for tests; nil uses a real dial + session.
	Conn Conn
}

// Conn is the minimal session surface the storage needs. Production uses
// realConn (dial + *ssh.Client); tests use a fake that records commands.
type Conn interface {
	// Run executes cmd on the remote, streaming stdin into it and returning
	// the combined stdout+stderr.
	Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, error)
	Close() error
}

// realConn dials a real SSH server and runs commands over a session.
type realConn struct {
	client *ssh.Client
}

func (c *realConn) Run(ctx context.Context, cmd string, stdin io.Reader) ([]byte, error) {
	sess, err := c.client.NewSession()
	if err != nil {
		return nil, err
	}
	defer sess.Close()
	var buf bytes.Buffer
	sess.Stdin = stdin
	sess.Stdout = &buf
	sess.Stderr = &buf
	if err := sess.Run(cmd); err != nil {
		return buf.Bytes(), err
	}
	return buf.Bytes(), nil
}

func (c *realConn) Close() error { return c.client.Close() }

type Storage struct {
	cfg Config

	mu   sync.Mutex
	conn Conn // cached, reused across calls; nil until first real connect
}

var _ core.Storage = (*Storage)(nil)

func New(cfg Config) (*Storage, error) {
	if cfg.Host == "" {
		return nil, fmt.Errorf("ssh storage: no host provided")
	}
	if cfg.User == "" {
		return nil, fmt.Errorf("ssh storage: no user provided")
	}
	if cfg.KeyPath == "" && cfg.Password == "" {
		return nil, fmt.Errorf("ssh storage: provide either a key_path or a password")
	}
	if cfg.Port == 0 {
		cfg.Port = 22
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	return &Storage{cfg: cfg}, nil
}

func (s *Storage) Scheme() string { return "ssh" }

// auth builds the ssh.AuthMethod for the configured credential.
func (s *Storage) auth() (ssh.AuthMethod, error) {
	if s.cfg.Password != "" {
		return ssh.Password(s.cfg.Password), nil
	}
	pem, err := os.ReadFile(s.cfg.KeyPath)
	if err != nil {
		return nil, fmt.Errorf("ssh storage: read key %s: %w", s.cfg.KeyPath, err)
	}
	signer, err := ssh.ParsePrivateKey(pem)
	if err != nil {
		return nil, fmt.Errorf("ssh storage: parse key %s: %w", s.cfg.KeyPath, err)
	}
	return ssh.PublicKeys(signer), nil
}

// connect returns a Conn for the configured server, dialing once and caching
// the connection for reuse across every Open/Save/Delete/Copy/List call made
// on this Storage instance. A fresh TCP dial + SSH handshake per file (the
// previous behavior) made uploading an HLS rendition ladder — hundreds of
// small segment files — dominated by reconnect latency instead of transfer
// time. Call Close when done with this Storage to release the connection.
func (s *Storage) connect(ctx context.Context) (Conn, error) {
	if s.cfg.Conn != nil {
		return s.cfg.Conn, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return s.conn, nil
	}
	auth, err := s.auth()
	if err != nil {
		return nil, err
	}
	addr := net.JoinHostPort(s.cfg.Host, fmt.Sprintf("%d", s.cfg.Port))
	clientCfg := &ssh.ClientConfig{
		User:            s.cfg.User,
		Auth:            []ssh.AuthMethod{auth},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), // matches StrictHostKeyChecking=no
		Timeout:         s.cfg.Timeout,
	}
	dialer := net.Dialer{Timeout: s.cfg.Timeout}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("ssh dial %s: %w", addr, err)
	}
	c, chans, reqs, err := ssh.NewClientConn(conn, addr, clientCfg)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh auth to %s: %w", addr, err)
	}
	s.conn = &realConn{client: ssh.NewClient(c, chans, reqs)}
	return s.conn, nil
}

// Close releases the cached SSH connection, if one was opened. Callers that
// build a Storage for a bounded operation (one task, one request) should
// close it when done — e.g. via an io.Closer type-assertion, since the core.Storage
// interface itself has no Close method and local/s3 have nothing to release.
func (s *Storage) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

func (s *Storage) remotePath(ref core.AssetRef) string {
	p := strings.TrimPrefix(ref.URI, "ssh:")
	if p == "" || p == ref.URI {
		p = ref.Name
	}
	return s.joinBase(p)
}

// RemotePath exposes the remote file path for a relative name under the
// configured base path (used by tests to assert the joined path).
func (s *Storage) RemotePath(name string) string {
	return s.joinBase(name)
}

func (s *Storage) joinBase(name string) string {
	return filepath.ToSlash(filepath.Join(s.cfg.BasePath, name))
}

func (s *Storage) Open(ctx context.Context, ref core.AssetRef) (io.ReadCloser, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	out, err := conn.Run(ctx, "cat '"+s.remotePath(ref)+"'", nil)
	if err != nil {
		return nil, fmt.Errorf("ssh cat failed: %w", err)
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

func (s *Storage) Save(ctx context.Context, ref core.AssetRef, r io.Reader) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	dir := filepath.ToSlash(filepath.Dir(s.remotePath(ref)))
	if _, err := conn.Run(ctx, "mkdir -p '"+dir+"'", nil); err != nil {
		return fmt.Errorf("ssh mkdir failed: %w", err)
	}
	if _, err := conn.Run(ctx, "cat > '"+s.remotePath(ref)+"'", r); err != nil {
		return fmt.Errorf("ssh write failed: %w", err)
	}
	return nil
}

func (s *Storage) Delete(ctx context.Context, ref core.AssetRef) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.Run(ctx, "rm -f '"+s.remotePath(ref)+"'", nil); err != nil {
		return fmt.Errorf("ssh rm failed: %w", err)
	}
	return nil
}

func (s *Storage) Copy(ctx context.Context, src, dst core.AssetRef) error {
	conn, err := s.connect(ctx)
	if err != nil {
		return err
	}
	if _, err := conn.Run(ctx, "cp '"+s.remotePath(src)+"' '"+s.remotePath(dst)+"'", nil); err != nil {
		return fmt.Errorf("ssh cp failed: %w", err)
	}
	return nil
}

func (s *Storage) Verify(ctx context.Context, ref core.AssetRef) (bool, error) {
	in, err := s.Open(ctx, ref)
	if err != nil {
		return false, err
	}
	defer in.Close()
	h := sha256.New()
	if _, err := io.Copy(h, in); err != nil {
		return false, err
	}
	sum := hex.EncodeToString(h.Sum(nil))
	return sum != "", nil
}

// List runs a find on the remote base path and returns relative paths.
func (s *Storage) List(ctx context.Context) ([]string, error) {
	conn, err := s.connect(ctx)
	if err != nil {
		return nil, err
	}
	out, err := conn.Run(ctx, "cd '"+s.cfg.BasePath+"' && find . -type f | sort", nil)
	if err != nil {
		return nil, fmt.Errorf("ssh find failed: %w", err)
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "." {
			continue
		}
		line = strings.TrimPrefix(line, "./")
		files = append(files, filepath.ToSlash(line))
	}
	return files, nil
}
