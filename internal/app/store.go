package app

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// timeFormat has a fixed width so that lexicographic order equals chronological
// order, which the ORDER BY clauses and the running-run sweep rely on.
const timeFormat = "2006-01-02T15:04:05.000000000Z"

func now() string { return time.Now().UTC().Format(timeFormat) }

// Run status values. RunInterrupted is written both as the status and as the
// error kind of a claimed run found unfinished at startup, so a client never has
// to special-case a run that is neither running nor failed for a stated reason.
const (
	RunRunning     = "running"
	RunSucceeded   = "succeeded"
	RunFailed      = "failed"
	RunInterrupted = "interrupted"
)

type Prompt struct {
	ID          int64    `json:"id"`
	Title       string   `json:"title"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags"`
	SourceRunID *string  `json:"source_run_id"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

type Scheme struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type Run struct {
	RequestID string `json:"request_id"`
	Input     string `json:"input"`
	// SchemeID is the submitted scheme selector, kept as a plain integer with no
	// foreign key: a run keeps identifying the scheme it used even after that
	// scheme row is deleted, which is what makes replaying the run possible.
	SchemeID       int64           `json:"scheme_id"`
	SchemeName     string          `json:"scheme_name"`
	SchemeSnapshot string          `json:"scheme_snapshot"`
	Model          string          `json:"model"`
	Params         json.RawMessage `json:"params"`
	Result         string          `json:"result"`
	Status         string          `json:"status"`
	ErrorKind      string          `json:"error_kind,omitempty"`
	CreatedAt      string          `json:"created_at"`
	FinishedAt     *string         `json:"finished_at,omitempty"`
}

type Store struct{ db *sql.DB }

func OpenStore(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	// One connection: the workload is a private single-user site and this keeps
	// writers serialized without SQLITE_BUSY handling.
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA foreign_keys=ON;
PRAGMA busy_timeout=5000;
CREATE TABLE IF NOT EXISTS optimization_runs (
 request_id TEXT PRIMARY KEY, input TEXT NOT NULL, scheme_id INTEGER NOT NULL,
 scheme_name TEXT NOT NULL, scheme_snapshot TEXT NOT NULL, model TEXT NOT NULL,
 params TEXT NOT NULL, result TEXT NOT NULL DEFAULT '', status TEXT NOT NULL,
 error_kind TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL, finished_at TEXT
);
CREATE TABLE IF NOT EXISTS prompts (
 id INTEGER PRIMARY KEY, title TEXT NOT NULL, body TEXT NOT NULL, tags TEXT NOT NULL DEFAULT '[]',
 source_run_id TEXT REFERENCES optimization_runs(request_id), created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS schemes (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, body TEXT NOT NULL, created_at TEXT NOT NULL, updated_at TEXT NOT NULL
);`); err != nil {
		db.Close()
		return nil, fmt.Errorf("initialize database: %w", err)
	}
	// A run left in "running" has no process behind it any more; nothing can
	// finish it and no client can observe a truthful status for it.
	if _, err = db.Exec(`UPDATE optimization_runs SET status=?,error_kind=?,finished_at=? WHERE status=?`,
		RunInterrupted, RunInterrupted, now(), RunRunning); err != nil {
		db.Close()
		return nil, fmt.Errorf("mark interrupted runs: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error { return s.db.Close() }

type scanner interface{ Scan(...any) error }

const promptColumns = `id,title,body,tags,source_run_id,created_at,updated_at`

func scanPrompt(row scanner) (Prompt, error) {
	var p Prompt
	var tags string
	if err := row.Scan(&p.ID, &p.Title, &p.Body, &tags, &p.SourceRunID, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return Prompt{}, err
	}
	if err := json.Unmarshal([]byte(tags), &p.Tags); err != nil {
		return Prompt{}, fmt.Errorf("decode tags: %w", err)
	}
	if p.Tags == nil {
		p.Tags = []string{}
	}
	return p, nil
}

func (s *Store) Prompt(id int64) (Prompt, error) {
	return scanPrompt(s.db.QueryRow(`SELECT `+promptColumns+` FROM prompts WHERE id=?`, id))
}

// Prompts returns prompts matching a case-insensitive substring of title or body
// and, when tag is non-empty, an exact tag. Newest update first.
func (s *Store) Prompts(query, tag string) ([]Prompt, error) {
	rows, err := s.db.Query(`SELECT `+promptColumns+` FROM prompts WHERE
 (?='' OR instr(lower(title),lower(?))>0 OR instr(lower(body),lower(?))>0)
 AND (?='' OR EXISTS (SELECT 1 FROM json_each(prompts.tags) WHERE value=?))
 ORDER BY updated_at DESC,id DESC`, query, query, query, tag, tag)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Prompt{}
	for rows.Next() {
		p, err := scanPrompt(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, p)
	}
	return items, rows.Err()
}

// ErrSourceRun means the referenced run is missing or did not succeed; a failed
// or unfinished run is not a legitimate provenance record.
var ErrSourceRun = errors.New("source run must have succeeded")

func encodeTags(tags []string) string {
	if tags == nil {
		tags = []string{}
	}
	b, err := json.Marshal(tags)
	if err != nil {
		return "[]"
	}
	return string(b)
}

func (s *Store) CreatePrompt(p Prompt) (Prompt, error) {
	if p.SourceRunID != nil {
		r, err := s.Run(*p.SourceRunID)
		if err != nil || r.Status != RunSucceeded {
			return Prompt{}, ErrSourceRun
		}
	}
	t := now()
	res, err := s.db.Exec(`INSERT INTO prompts(title,body,tags,source_run_id,created_at,updated_at) VALUES(?,?,?,?,?,?)`,
		p.Title, p.Body, encodeTags(p.Tags), p.SourceRunID, t, t)
	if err != nil {
		return Prompt{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Prompt{}, err
	}
	return s.Prompt(id)
}

func (s *Store) UpdatePrompt(p Prompt) (Prompt, error) {
	res, err := s.db.Exec(`UPDATE prompts SET title=?,body=?,tags=?,updated_at=? WHERE id=?`,
		p.Title, p.Body, encodeTags(p.Tags), now(), p.ID)
	if err != nil {
		return Prompt{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Prompt{}, sql.ErrNoRows
	}
	return s.Prompt(p.ID)
}

func (s *Store) DeletePrompt(id int64) error {
	return deleteRow(s.db, `DELETE FROM prompts WHERE id=?`, id)
}

func scanScheme(row scanner) (Scheme, error) {
	var v Scheme
	err := row.Scan(&v.ID, &v.Name, &v.Body, &v.CreatedAt, &v.UpdatedAt)
	return v, err
}

const schemeColumns = `id,name,body,created_at,updated_at`

func (s *Store) Scheme(id int64) (Scheme, error) {
	return scanScheme(s.db.QueryRow(`SELECT `+schemeColumns+` FROM schemes WHERE id=?`, id))
}

func (s *Store) Schemes() ([]Scheme, error) {
	rows, err := s.db.Query(`SELECT ` + schemeColumns + ` FROM schemes ORDER BY updated_at DESC,id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Scheme{}
	for rows.Next() {
		v, err := scanScheme(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, v)
	}
	return items, rows.Err()
}

func (s *Store) CreateScheme(v Scheme) (Scheme, error) {
	t := now()
	res, err := s.db.Exec(`INSERT INTO schemes(name,body,created_at,updated_at) VALUES(?,?,?,?)`, v.Name, v.Body, t, t)
	if err != nil {
		return Scheme{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Scheme{}, err
	}
	return s.Scheme(id)
}

func (s *Store) UpdateScheme(v Scheme) (Scheme, error) {
	res, err := s.db.Exec(`UPDATE schemes SET name=?,body=?,updated_at=? WHERE id=?`, v.Name, v.Body, now(), v.ID)
	if err != nil {
		return Scheme{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Scheme{}, sql.ErrNoRows
	}
	return s.Scheme(v.ID)
}

func (s *Store) DeleteScheme(id int64) error {
	return deleteRow(s.db, `DELETE FROM schemes WHERE id=?`, id)
}

func deleteRow(db *sql.DB, query string, args ...any) error {
	res, err := db.Exec(query, args...)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

const runColumns = `request_id,input,scheme_id,scheme_name,scheme_snapshot,model,params,result,status,error_kind,created_at,finished_at`

func (s *Store) Run(id string) (Run, error) {
	var r Run
	var params string
	err := s.db.QueryRow(`SELECT `+runColumns+` FROM optimization_runs WHERE request_id=?`, id).
		Scan(&r.RequestID, &r.Input, &r.SchemeID, &r.SchemeName, &r.SchemeSnapshot, &r.Model, &params,
			&r.Result, &r.Status, &r.ErrorKind, &r.CreatedAt, &r.FinishedAt)
	if err != nil {
		return Run{}, err
	}
	r.Params = json.RawMessage(params)
	return r, nil
}

// ClaimRun claims the request ID with the parameters that are about to be sent.
// It reports false when a run with that ID already exists, in which case only the
// caller that got true may call upstream. The insert is a single atomic
// statement, so concurrent duplicates cannot both win.
func (s *Store) ClaimRun(r Run) (bool, error) {
	params := r.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	res, err := s.db.Exec(`INSERT INTO optimization_runs(request_id,input,scheme_id,scheme_name,scheme_snapshot,model,params,status,created_at)
 VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(request_id) DO NOTHING`,
		r.RequestID, r.Input, r.SchemeID, r.SchemeName, r.SchemeSnapshot, r.Model, string(params), RunRunning, now())
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// FinishRun settles a run that is still running. A run already settled by
// another path (interrupted at startup, second writer) is left untouched.
func (s *Store) FinishRun(id, status, result, errorKind string) error {
	_, err := s.db.Exec(`UPDATE optimization_runs SET status=?,result=?,error_kind=?,finished_at=? WHERE request_id=? AND status=?`,
		status, result, errorKind, now(), id, RunRunning)
	return err
}
