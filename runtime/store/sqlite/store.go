// Package sqlite implements core.Store on a single SQLite file with WAL mode.
// Every state change is committed together with its Event in ONE transaction —
// this is what makes Event Sourcing durable across crashes.
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go driver, no CGO
	"github.com/mohammadjaf013/weft/core"
)

type Store struct {
	db *sql.DB
}

var _ core.Store = (*Store)(nil)

// Open opens (or creates) the SQLite database at path and applies the schema.
func Open(path string) (*Store, error) {
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // SQLite single-writer; serializing avoids lock contention
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

func OpenInMemory() (*Store, error) {
	return Open(":memory:")
}

func migrate(db *sql.DB) error {
	const schema = `
CREATE TABLE IF NOT EXISTS jobs (
    id              TEXT PRIMARY KEY,
    status          TEXT NOT NULL,
    priority        TEXT NOT NULL DEFAULT 'normal',
    profile         TEXT,
    destination_id  INTEGER,
    dest_path       TEXT NOT NULL DEFAULT '',
    input_ref       TEXT NOT NULL,
    delete_source   INTEGER NOT NULL DEFAULT 0,
    verified        INTEGER NOT NULL DEFAULT 0,
    error           TEXT,
    created_at      TEXT NOT NULL,
    updated_at      TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_jobs_status ON jobs(status);

CREATE TABLE IF NOT EXISTS tasks (
    id                TEXT PRIMARY KEY,
    job_id            TEXT NOT NULL,
    kind              TEXT NOT NULL,
    status            TEXT NOT NULL,
    progress_percent  REAL NOT NULL DEFAULT 0,
    depends_on        TEXT,
    worker_id         TEXT,
    lease_expires_at  TEXT,
    started_at        TEXT,
    finished_at       TEXT,
    error             TEXT,
    params            TEXT
);
CREATE INDEX IF NOT EXISTS idx_tasks_job ON tasks(job_id);
CREATE INDEX IF NOT EXISTS idx_tasks_status ON tasks(status);

CREATE TABLE IF NOT EXISTS events (
    id          TEXT PRIMARY KEY,
    job_id      TEXT,
    task_id     TEXT,
    event       TEXT NOT NULL,
    payload     TEXT NOT NULL,
    created_at  TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_events_job ON events(job_id);

CREATE TABLE IF NOT EXISTS task_outputs (
    task_id   TEXT NOT NULL,
    job_id    TEXT NOT NULL,
    kind      TEXT NOT NULL,
    name      TEXT NOT NULL,
    uri       TEXT NOT NULL,
    bytes     INTEGER NOT NULL DEFAULT 0,
    dir       TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (task_id, name)
);
CREATE INDEX IF NOT EXISTS idx_task_outputs_job ON task_outputs(job_id);

CREATE TABLE IF NOT EXISTS outbox (
    id                TEXT PRIMARY KEY,
    event_id          TEXT NOT NULL,
    webhook_id        TEXT NOT NULL,
    status            TEXT NOT NULL DEFAULT 'pending',
    attempts          INTEGER NOT NULL DEFAULT 0,
    next_attempt_at   TEXT,
    created_at        TEXT NOT NULL,
    delivered_at      TEXT
);
CREATE INDEX IF NOT EXISTS idx_outbox_status ON outbox(status, next_attempt_at);

CREATE TABLE IF NOT EXISTS workers (
    id                  TEXT PRIMARY KEY,
    status              TEXT NOT NULL,
    current_task_id     TEXT,
    last_heartbeat_at   TEXT
);

CREATE TABLE IF NOT EXISTS storage_servers (
    id          INTEGER PRIMARY KEY,
    type        TEXT NOT NULL,
    host        TEXT,
    user        TEXT,
    config      TEXT,
    created_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS ssh_keys (
    id                      TEXT PRIMARY KEY,
    public_key              TEXT NOT NULL,
    private_key_encrypted   BLOB NOT NULL,
    active                  INTEGER NOT NULL DEFAULT 1,
    created_at              TEXT NOT NULL,
    rotated_at              TEXT
);

CREATE TABLE IF NOT EXISTS api_keys (
    id            TEXT PRIMARY KEY,
    name          TEXT NOT NULL,
    key_hash      TEXT NOT NULL,
    scopes        TEXT NOT NULL,
    expires_at    TEXT,
    ip_allowlist  TEXT,
    created_at    TEXT NOT NULL,
    revoked_at    TEXT
);

CREATE TABLE IF NOT EXISTS webhooks (
    id                TEXT PRIMARY KEY,
    url               TEXT NOT NULL,
    secret            TEXT NOT NULL,
    events            TEXT NOT NULL,
    max_retries       INTEGER NOT NULL DEFAULT 8,
    timeout_seconds   INTEGER NOT NULL DEFAULT 10,
    created_at        TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS benchmarks (
    id                    TEXT PRIMARY KEY,
    node_id               TEXT NOT NULL,
    cpu_score             REAL,
    ffmpeg_score          REAL,
    disk_io_score         REAL,
    memory_score          REAL,
    estimated_capacity    REAL,
    created_at            TEXT NOT NULL
);
`
	_, err := db.Exec(schema)
	if err != nil {
		return err
	}
	// Migrations for databases created before these columns existed.
	return migrateColumns(db)
}

// migrateColumns adds columns that newer schema versions introduced, ignoring
// "duplicate column" errors so the step is idempotent across restarts.
func migrateColumns(db *sql.DB) error {
	stmts := []string{
		"ALTER TABLE tasks ADD COLUMN params TEXT",
		"ALTER TABLE task_outputs ADD COLUMN dir TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE jobs ADD COLUMN dest_path TEXT NOT NULL DEFAULT ''",
		"ALTER TABLE jobs ADD COLUMN delete_source INTEGER NOT NULL DEFAULT 0",
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			return err
		}
	}
	return nil
}

func (s *Store) Close() error { return s.db.Close() }

// Snapshot returns job-count gauges for metrics (queued, running, completed, failed).
func (s *Store) Snapshot() (queued, running, completed, failed uint64) {
	row := s.db.QueryRow(`SELECT
		SUM(status='queued'), SUM(status='running'), SUM(status='completed'), SUM(status='failed')
		FROM jobs`)
	var q, r, c, f sql.NullInt64
	_ = row.Scan(&q, &r, &c, &f)
	return uint64(q.Int64), uint64(r.Int64), uint64(c.Int64), uint64(f.Int64)
}

func (s *Store) DB() *sql.DB { return s.db }

// ts formats a timestamp with a fixed-width fractional second so that string
// comparison in SQL WHERE clauses is lexicographically correct. RFC3339Nano
// strips trailing zeros, which breaks `next_attempt_at <= ?` ordering.
const tsLayout = "2006-01-02T15:04:05.000000000Z"

func ts(t time.Time) string { return t.UTC().Format(tsLayout) }

func parseTS(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(tsLayout, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func (s *Store) SaveJob(ctx context.Context, j core.Job) error {
	return s.SaveJobEvent(ctx, j, core.Event{})
}

func (s *Store) SaveJobEvent(ctx context.Context, j core.Job, e core.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	verified := 0
	if j.Verified {
		verified = 1
	}
	delSrc := 0
	if j.DeleteSource {
		delSrc = 1
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO jobs(id,status,priority,profile,destination_id,dest_path,input_ref,delete_source,verified,error,created_at,updated_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, priority=excluded.priority, profile=excluded.profile,
		 destination_id=excluded.destination_id, dest_path=excluded.dest_path, input_ref=excluded.input_ref,
		 delete_source=excluded.delete_source, verified=excluded.verified, error=excluded.error, updated_at=excluded.updated_at`,
		j.ID, string(j.Status), string(j.Priority), j.Profile, j.DestinationID, j.DestPath, j.InputRef, delSrc, verified, j.Error, ts(j.CreatedAt), ts(j.UpdatedAt)); err != nil {
		return err
	}
	if e.ID != "" {
		if err := insertEventTx(ctx, tx, e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *Store) LoadJob(ctx context.Context, id core.JobID) (core.Job, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,status,priority,profile,destination_id,dest_path,input_ref,delete_source,verified,error,created_at,updated_at FROM jobs WHERE id=?`, id)
	var j core.Job
	var status, priority string
	var verified, delSrc int
	var created, updated string
	err := row.Scan(&j.ID, &status, &priority, &j.Profile, &j.DestinationID, &j.DestPath, &j.InputRef, &delSrc, &verified, &j.Error, &created, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return core.Job{}, core.ErrJobNotFound
	}
	if err != nil {
		return core.Job{}, err
	}
	j.Status = core.JobStatus(status)
	j.Priority = core.Priority(priority)
	j.Verified = verified == 1
	j.DeleteSource = delSrc == 1
	j.CreatedAt = parseTS(created)
	j.UpdatedAt = parseTS(updated)
	return j, nil
}

func (s *Store) ListJobs(ctx context.Context, filter core.JobFilter) ([]core.Job, error) {
	q := `SELECT id,status,priority,profile,destination_id,dest_path,input_ref,delete_source,verified,error,created_at,updated_at FROM jobs`
	var args []any
	where := ""
	if filter.Status != "" {
		where += ` WHERE status=?`
		args = append(args, string(filter.Status))
	}
	if filter.Priority != "" {
		if where == "" {
			where = ` WHERE`
		} else {
			where += ` AND`
		}
		where += ` priority=?`
		args = append(args, string(filter.Priority))
	}
	if filter.Cursor != "" {
		if where == "" {
			where = ` WHERE`
		} else {
			where += ` AND`
		}
		where += ` created_at < ?`
		args = append(args, filter.Cursor)
	}
	if filter.Limit > 0 {
		where += ` LIMIT ?`
		args = append(args, filter.Limit)
	}
	rows, err := s.db.QueryContext(ctx, q+where+` ORDER BY created_at DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Job{}
	for rows.Next() {
		var j core.Job
		var status, priority string
		var verified, delSrc int
		var created, updated string
		if err := rows.Scan(&j.ID, &status, &priority, &j.Profile, &j.DestinationID, &j.DestPath, &j.InputRef, &delSrc, &verified, &j.Error, &created, &updated); err != nil {
			return nil, err
		}
		j.Status = core.JobStatus(status)
		j.Priority = core.Priority(priority)
		j.Verified = verified == 1
		j.DeleteSource = delSrc == 1
		j.CreatedAt = parseTS(created)
		j.UpdatedAt = parseTS(updated)
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) SaveTask(ctx context.Context, t core.Task) error {
	return s.SaveTaskEvent(ctx, t, core.Event{})
}

// UpdateTaskProgress writes only status + progress_percent for a task. It must
// not touch worker_id/lease/started/error so the reservation metadata survives
// high-frequency progress callbacks.
func (s *Store) UpdateTaskProgress(ctx context.Context, id core.TaskID, status core.TaskStatus, progress float64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tasks SET status=?, progress_percent=? WHERE id=?`,
		string(status), progress, id)
	return err
}

func (s *Store) SaveTaskEvent(ctx context.Context, t core.Task, e core.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	deps, _ := json.Marshal(t.DependsOn)
	params, _ := json.Marshal(t.Params)
	lease, started, finished := "", "", ""
	if t.LeaseExpiresAt != nil {
		lease = ts(*t.LeaseExpiresAt)
	}
	if t.StartedAt != nil {
		started = ts(*t.StartedAt)
	}
	if t.FinishedAt != nil {
		finished = ts(*t.FinishedAt)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO tasks(id,job_id,kind,status,progress_percent,depends_on,worker_id,lease_expires_at,started_at,finished_at,error,params)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET status=excluded.status, progress_percent=excluded.progress_percent,
		 worker_id=excluded.worker_id, lease_expires_at=excluded.lease_expires_at, started_at=excluded.started_at,
		 finished_at=excluded.finished_at, error=excluded.error`,
		t.ID, t.JobID, t.Kind, string(t.Status), t.Progress, string(deps), t.WorkerID, lease, started, finished, t.Error, string(params)); err != nil {
		return err
	}
	if e.ID != "" {
		if err := insertEventTx(ctx, tx, e); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func insertEventTx(ctx context.Context, tx *sql.Tx, e core.Event) error {
	payload := string(e.Payload)
	if payload == "" {
		payload = "{}"
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO events(id,job_id,task_id,event,payload,created_at) VALUES(?,?,?,?,?,?)`,
		e.ID, e.JobID, e.TaskID, string(e.Kind), payload, ts(e.CreatedAt))
	return err
}

func (s *Store) LoadTask(ctx context.Context, id core.TaskID) (core.Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id,job_id,kind,status,progress_percent,depends_on,worker_id,lease_expires_at,started_at,finished_at,error,params FROM tasks WHERE id=?`, id)
	var t core.Task
	var status, deps, lease, started, finished string
	var params string
	err := row.Scan(&t.ID, &t.JobID, &t.Kind, &status, &t.Progress, &deps, &t.WorkerID, &lease, &started, &finished, &t.Error, &params)
	if err != nil {
		return core.Task{}, err
	}
	t.Status = core.TaskStatus(status)
	_ = json.Unmarshal([]byte(deps), &t.DependsOn)
	_ = json.Unmarshal([]byte(params), &t.Params)
	if lease != "" {
		lt := parseTS(lease)
		t.LeaseExpiresAt = &lt
	}
	if started != "" {
		st := parseTS(started)
		t.StartedAt = &st
	}
	if finished != "" {
		ft := parseTS(finished)
		t.FinishedAt = &ft
	}
	return t, nil
}

func (s *Store) ListTasks(ctx context.Context, jobID core.JobID) ([]core.Task, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,job_id,kind,status,progress_percent,depends_on,worker_id,lease_expires_at,started_at,finished_at,error,params FROM tasks WHERE job_id=?`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Task{}
	for rows.Next() {
		var t core.Task
		var status, deps, lease, started, finished string
		var params string
		if err := rows.Scan(&t.ID, &t.JobID, &t.Kind, &status, &t.Progress, &deps, &t.WorkerID, &lease, &started, &finished, &t.Error, &params); err != nil {
			return nil, err
		}
		t.Status = core.TaskStatus(status)
		_ = json.Unmarshal([]byte(deps), &t.DependsOn)
		_ = json.Unmarshal([]byte(params), &t.Params)
		if lease != "" {
			lt := parseTS(lease)
			t.LeaseExpiresAt = &lt
		}
		if started != "" {
			st := parseTS(started)
			t.StartedAt = &st
		}
		if finished != "" {
			ft := parseTS(finished)
			t.FinishedAt = &ft
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) AppendEvent(ctx context.Context, e core.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertEventTx(ctx, tx, e); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListEvents(ctx context.Context, jobID core.JobID) ([]core.Event, error) {
	q := `SELECT id,job_id,task_id,event,payload,created_at FROM events`
	var args []any
	if jobID != "" {
		q += ` WHERE job_id=?`
		args = append(args, jobID)
	}
	q += ` ORDER BY created_at`
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.Event{}
	for rows.Next() {
		var e core.Event
		var kind, payload, created string
		if err := rows.Scan(&e.ID, &e.JobID, &e.TaskID, &kind, &payload, &created); err != nil {
			return nil, err
		}
		e.Kind = core.EventKind(kind)
		e.Payload = json.RawMessage(payload)
		e.CreatedAt = parseTS(created)
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) Outbox() core.OutboxStore { return &outboxStore{db: s.db} }

type outboxStore struct{ db *sql.DB }

func (o *outboxStore) Enqueue(ctx context.Context, eventID, webhookID string, at time.Time) error {
	_, err := o.db.ExecContext(ctx, `INSERT INTO outbox(id,event_id,webhook_id,status,attempts,next_attempt_at,created_at)
		VALUES(?,?,?, 'pending', 0, ?, ?)`,
		core.NewID("ob"), eventID, webhookID, ts(at), ts(core.Now()))
	return err
}

func (o *outboxStore) ListPending(ctx context.Context, limit int, now time.Time) ([]core.OutboxRow, error) {
	rows, err := o.db.QueryContext(ctx, `SELECT id,event_id,webhook_id,status,attempts,next_attempt_at,created_at,delivered_at
		FROM outbox WHERE status='pending' AND next_attempt_at <= ? ORDER BY next_attempt_at LIMIT ?`, ts(now), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.OutboxRow{}
	for rows.Next() {
		var r core.OutboxRow
		var next, created string
		var delivered sql.NullString
		if err := rows.Scan(&r.ID, &r.EventID, &r.WebhookID, &r.Status, &r.Attempts, &next, &created, &delivered); err != nil {
			return nil, err
		}
		r.NextAttemptAt = parseTS(next)
		r.CreatedAt = parseTS(created)
		if delivered.Valid {
			d := parseTS(delivered.String)
			r.DeliveredAt = &d
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (o *outboxStore) Mark(ctx context.Context, id string, delivered bool, errMsg string) error {
	if delivered {
		_, err := o.db.ExecContext(ctx, `UPDATE outbox SET status='delivered', delivered_at=?, attempts=attempts+1 WHERE id=?`, ts(core.Now()), id)
		return err
	}
	_, err := o.db.ExecContext(ctx, `UPDATE outbox SET attempts=attempts+1 WHERE id=?`, id)
	return err
}

// DeadLetter marks a row whose retry budget is exhausted.
func (o *outboxStore) DeadLetter(ctx context.Context, id string) error {
	_, err := o.db.ExecContext(ctx, `UPDATE outbox SET status='dead_letter', attempts=attempts+1 WHERE id=?`, id)
	return err
}

// NextAttempt reschedules a pending row for later retry (exponential backoff).
func (o *outboxStore) NextAttempt(ctx context.Context, id string, at time.Time) error {
	_, err := o.db.ExecContext(ctx, `UPDATE outbox SET next_attempt_at=?, attempts=attempts+1 WHERE id=?`, ts(at), id)
	return err
}

// ResetToPending requeues a dead-lettered row (CLI: weft webhooks replay).
func (o *outboxStore) ResetToPending(ctx context.Context, id string) error {
	_, err := o.db.ExecContext(ctx, `UPDATE outbox SET status='pending', attempts=0, next_attempt_at=? WHERE id=?`, ts(core.Now()), id)
	return err
}

// SaveTaskOutputs records the assets a task produced, keyed by (task, name).
func (s *Store) SaveTaskOutputs(ctx context.Context, taskID core.TaskID, jobID core.JobID, assets []core.AssetRef) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, a := range assets {
		if _, err := tx.ExecContext(ctx, `INSERT INTO task_outputs(task_id,job_id,kind,name,uri,bytes,dir) VALUES(?,?,?,?,?,?,?)
			ON CONFLICT(task_id,name) DO UPDATE SET uri=excluded.uri, bytes=excluded.bytes, dir=excluded.dir`,
			taskID, jobID, a.Kind, a.Name, a.URI, a.Bytes, a.Dir); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// ListJobOutputs returns every asset produced by any task of the job.
func (s *Store) ListJobOutputs(ctx context.Context, jobID core.JobID) ([]core.AssetRef, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT kind,name,uri,bytes,dir FROM task_outputs WHERE job_id=? ORDER BY name`, jobID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []core.AssetRef{}
	for rows.Next() {
		var a core.AssetRef
		if err := rows.Scan(&a.Kind, &a.Name, &a.URI, &a.Bytes, &a.Dir); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// RequeueExpired resets tasks whose lease has expired and whose owner is gone
// back to ready so a healthy worker can pick them up. Returns requeued ids.
func (s *Store) RequeueExpired(ctx context.Context, now time.Time) ([]core.TaskID, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM tasks WHERE lease_expires_at IS NOT NULL
		AND lease_expires_at < ? AND status IN ('leased','running')`, ts(now))
	if err != nil {
		return nil, err
	}
	var ids []core.TaskID
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, core.TaskID(id))
	}
	rows.Close()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE tasks SET status='ready', worker_id='', started_at='', lease_expires_at='' WHERE id=?`, id); err != nil {
			return nil, err
		}
	}
	return ids, nil
}
