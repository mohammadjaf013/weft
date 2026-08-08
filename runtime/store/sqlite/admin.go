package sqlite

import (
	"context"
	"encoding/json"
	"time"

	"github.com/mohammadjaf013/weft/core"
)

// --- Webhooks ---

type Webhook struct {
	ID             string
	URL            string
	Secret         string
	Events         []string // dot.lowercase wire names, e.g. "job.started"
	MaxRetries     int
	TimeoutSeconds int
	CreatedAt      time.Time
}

func (s *Store) SaveWebhook(ctx context.Context, w Webhook) error {
	evs, _ := json.Marshal(w.Events)
	_, err := s.db.ExecContext(ctx, `INSERT INTO webhooks(id,url,secret,events,max_retries,timeout_seconds,created_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET url=excluded.url, secret=excluded.secret, events=excluded.events,
		 max_retries=excluded.max_retries, timeout_seconds=excluded.timeout_seconds`,
		w.ID, w.URL, w.Secret, string(evs), w.MaxRetries, w.TimeoutSeconds, ts(w.CreatedAt))
	return err
}

func (s *Store) LoadWebhook(ctx context.Context, id string) (Webhook, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,url,secret,events,max_retries,timeout_seconds,created_at FROM webhooks WHERE id=?`, id)
	var w Webhook
	var evs, created string
	if err := row.Scan(&w.ID, &w.URL, &w.Secret, &evs, &w.MaxRetries, &w.TimeoutSeconds, &created); err != nil {
		return Webhook{}, err
	}
	_ = json.Unmarshal([]byte(evs), &w.Events)
	w.CreatedAt = parseTS(created)
	return w, nil
}

func (s *Store) ListWebhooks(ctx context.Context) ([]Webhook, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,url,secret,events,max_retries,timeout_seconds,created_at FROM webhooks ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Webhook{}
	for rows.Next() {
		var w Webhook
		var evs, created string
		if err := rows.Scan(&w.ID, &w.URL, &w.Secret, &evs, &w.MaxRetries, &w.TimeoutSeconds, &created); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(evs), &w.Events)
		w.CreatedAt = parseTS(created)
		out = append(out, w)
	}
	return out, rows.Err()
}

func (s *Store) DeleteWebhook(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM webhooks WHERE id=?`, id)
	return err
}

// --- API Keys ---

type APIKey struct {
	ID          string
	Name        string
	KeyHash     string
	Scopes      []string
	ExpiresAt   *time.Time
	IPAllowlist []string
	CreatedAt   time.Time
	RevokedAt   *time.Time
}

func (s *Store) SaveAPIKey(ctx context.Context, k APIKey) error {
	scopes, _ := json.Marshal(k.Scopes)
	ips, _ := json.Marshal(k.IPAllowlist)
	exp, rev := "", ""
	if k.ExpiresAt != nil {
		exp = ts(*k.ExpiresAt)
	}
	if k.RevokedAt != nil {
		rev = ts(*k.RevokedAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,scopes,expires_at,ip_allowlist,created_at,revoked_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		k.ID, k.Name, k.KeyHash, string(scopes), exp, string(ips), ts(k.CreatedAt), rev)
	return err
}

func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,key_hash,scopes,expires_at,ip_allowlist,created_at,revoked_at FROM api_keys ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []APIKey{}
	for rows.Next() {
		var k APIKey
		var scopes, ips, exp, rev, created string
		if err := rows.Scan(&k.ID, &k.Name, &k.KeyHash, &scopes, &exp, &ips, &created, &rev); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(scopes), &k.Scopes)
		_ = json.Unmarshal([]byte(ips), &k.IPAllowlist)
		k.CreatedAt = parseTS(created)
		if exp != "" {
			e := parseTS(exp)
			k.ExpiresAt = &e
		}
		if rev != "" {
			r := parseTS(rev)
			k.RevokedAt = &r
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func (s *Store) DeleteAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM api_keys WHERE id=?`, id)
	return err
}

func (s *Store) RevokeAPIKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE api_keys SET revoked_at=? WHERE id=?`, ts(core.Now()), id)
	return err
}

// --- Storage servers ---

type StorageServer struct {
	ID        int
	Type      string // ssh|local|s3|minio|r2
	Host      string
	User      string
	Config    map[string]any
	CreatedAt time.Time
}

func (s *Store) SaveStorageServer(ctx context.Context, sv StorageServer) error {
	cfg, _ := json.Marshal(sv.Config)
	_, err := s.db.ExecContext(ctx, `INSERT INTO storage_servers(id,type,host,user,config,created_at)
		VALUES(?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET type=excluded.type, host=excluded.host, user=excluded.user, config=excluded.config`,
		sv.ID, sv.Type, sv.Host, sv.User, string(cfg), ts(sv.CreatedAt))
	return err
}

func (s *Store) ListStorageServers(ctx context.Context) ([]StorageServer, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,type,host,user,config,created_at FROM storage_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StorageServer{}
	for rows.Next() {
		var sv StorageServer
		var cfg, created string
		if err := rows.Scan(&sv.ID, &sv.Type, &sv.Host, &sv.User, &cfg, &created); err != nil {
			return nil, err
		}
		sv.Config = map[string]any{}
		_ = json.Unmarshal([]byte(cfg), &sv.Config)
		sv.CreatedAt = parseTS(created)
		out = append(out, sv)
	}
	return out, rows.Err()
}

// --- Workers ---

type Worker struct {
	ID              string
	Status          string // idle|busy|offline
	CurrentTaskID   string
	LastHeartbeatAt time.Time
}

func (s *Store) SaveWorker(ctx context.Context, w Worker) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO workers(id,status,current_task_id,last_heartbeat_at)
		VALUES(?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, current_task_id=excluded.current_task_id,
		 last_heartbeat_at=excluded.last_heartbeat_at`,
		w.ID, w.Status, w.CurrentTaskID, ts(w.LastHeartbeatAt))
	return err
}

func (s *Store) ListWorkers(ctx context.Context) ([]Worker, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,status,current_task_id,last_heartbeat_at FROM workers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Worker{}
	for rows.Next() {
		var w Worker
		var hb string
		if err := rows.Scan(&w.ID, &w.Status, &w.CurrentTaskID, &hb); err != nil {
			return nil, err
		}
		w.LastHeartbeatAt = parseTS(hb)
		out = append(out, w)
	}
	return out, rows.Err()
}

// --- Benchmarks ---

type Benchmark struct {
	ID                string
	NodeID            string
	CPUScore          float64
	FFmpegScore       float64
	DiskIOScore       float64
	MemoryScore       float64
	EstimatedCapacity float64
	CreatedAt         time.Time
}

func (s *Store) SaveBenchmark(ctx context.Context, b Benchmark) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO benchmarks(id,node_id,cpu_score,ffmpeg_score,disk_io_score,memory_score,estimated_capacity,created_at)
		VALUES(?,?,?,?,?,?,?,?)`,
		b.ID, b.NodeID, b.CPUScore, b.FFmpegScore, b.DiskIOScore, b.MemoryScore, b.EstimatedCapacity, ts(b.CreatedAt))
	return err
}

func (s *Store) LastBenchmark(ctx context.Context) (Benchmark, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,node_id,cpu_score,ffmpeg_score,disk_io_score,memory_score,estimated_capacity,created_at
		FROM benchmarks ORDER BY created_at DESC LIMIT 1`)
	var b Benchmark
	var created string
	err := row.Scan(&b.ID, &b.NodeID, &b.CPUScore, &b.FFmpegScore, &b.DiskIOScore, &b.MemoryScore, &b.EstimatedCapacity, &created)
	if err != nil {
		return Benchmark{}, err
	}
	b.CreatedAt = parseTS(created)
	return b, nil
}

// --- SSH keys ---

type SSHKey struct {
	ID                  string
	PublicKey           string
	PrivateKeyEncrypted []byte
	Active              bool
	CreatedAt           time.Time
	RotatedAt           *time.Time
}

func (s *Store) SaveSSHKey(ctx context.Context, k SSHKey) error {
	active := 0
	if k.Active {
		active = 1
	}
	rot := ""
	if k.RotatedAt != nil {
		rot = ts(*k.RotatedAt)
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO ssh_keys(id,public_key,private_key_encrypted,active,created_at,rotated_at)
		VALUES(?,?,?,?,?,?)`,
		k.ID, k.PublicKey, k.PrivateKeyEncrypted, active, ts(k.CreatedAt), rot)
	return err
}

func (s *Store) ActiveSSHKey(ctx context.Context) (SSHKey, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,public_key,private_key_encrypted,active,created_at,rotated_at FROM ssh_keys WHERE active=1 ORDER BY created_at DESC LIMIT 1`)
	var k SSHKey
	var created, rot string
	var active int
	if err := row.Scan(&k.ID, &k.PublicKey, &k.PrivateKeyEncrypted, &active, &created, &rot); err != nil {
		return SSHKey{}, err
	}
	k.Active = active == 1
	k.CreatedAt = parseTS(created)
	if rot != "" {
		r := parseTS(rot)
		k.RotatedAt = &r
	}
	return k, nil
}

func (s *Store) DeactivateSSHKey(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE ssh_keys SET active=0, rotated_at=? WHERE id=?`, ts(core.Now()), id)
	return err
}
