package main

// SQLite access. The schema is identical to the one the original Node server created,
// so an existing data/crm.db keeps working.

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // pure-Go SQLite driver (no cgo, cross-compiles cleanly)
)

const schema = `
CREATE TABLE IF NOT EXISTS accounts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL UNIQUE,
  arr REAL NOT NULL DEFAULT 0,
  term_end TEXT,
  autorenew INTEGER,
  account_owner TEXT NOT NULL DEFAULT '',
  account_link TEXT NOT NULL DEFAULT '',
  account_owner_link TEXT NOT NULL DEFAULT '',
  slack_channel TEXT NOT NULL DEFAULT '',
  details TEXT NOT NULL DEFAULT '',
  mtm_comments TEXT NOT NULL DEFAULT '',
  mtm_synced_text TEXT,
  renewal_manager_link TEXT NOT NULL DEFAULT '',
  renewal_status TEXT NOT NULL DEFAULT '',
  ews TEXT NOT NULL DEFAULT '',
  renewal_manager TEXT NOT NULL DEFAULT '',
  last_email TEXT,
  last_gong TEXT,
  source_active INTEGER NOT NULL DEFAULT 1,
  dropped_at TEXT,
  risk TEXT,
  cadence TEXT,
  renewal_manager_override TEXT,
  contract_outcome TEXT,
  contract_new_arr REAL,
  contract_new_term_end TEXT,
  contract_new_autorenew INTEGER,
  contract_updated_at TEXT,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  ai_updated_at TEXT,
  manual_updated_at TEXT
);
CREATE TABLE IF NOT EXISTS activity (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id INTEGER,
  account_name TEXT,
  field TEXT NOT NULL,
  old_value TEXT,
  new_value TEXT,
  at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS activity_account ON activity(account_id);
CREATE TABLE IF NOT EXISTS notes (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  account_id INTEGER NOT NULL,
  body TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS notes_account ON notes(account_id);
CREATE TABLE IF NOT EXISTS buckets (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  description TEXT NOT NULL DEFAULT '',
  color TEXT NOT NULL DEFAULT '#3c6fd6',
  kind TEXT NOT NULL DEFAULT 'rule',
  position INTEGER NOT NULL DEFAULT 0,
  autorenew TEXT NOT NULL DEFAULT 'any',
  arr_min REAL,
  arr_max REAL,
  days_min INTEGER,
  days_max INTEGER
);
CREATE TABLE IF NOT EXISTS api_keys (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  name TEXT NOT NULL,
  key_hash TEXT NOT NULL,
  prefix TEXT NOT NULL,
  created_at TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT);
`

const accountCols = `id, name, arr, term_end, autorenew, account_owner, renewal_status, ews, renewal_manager,
  last_email, last_gong, source_active, dropped_at, risk, cadence,
  renewal_manager_override, contract_outcome, contract_new_arr, contract_new_term_end,
  contract_new_autorenew, contract_updated_at, created_at, updated_at, ai_updated_at, manual_updated_at,
  account_link, account_owner_link, slack_channel, details, renewal_manager_link, mtm_comments, mtm_synced_text`

// querier is satisfied by both *sql.DB and *sql.Tx.
type querier interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

func openDB(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1) // one writer; keeps transactions simple and avoids SQLITE_BUSY
	if _, err := db.Exec(schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	// Columns added after the first release: add them to databases created before that.
	for _, c := range []string{"account_link", "account_owner_link", "slack_channel", "details", "renewal_manager_link", "mtm_comments"} {
		if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN " + c + " TEXT NOT NULL DEFAULT ''"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("adding column %s: %w", c, err)
		}
	}
	// The channel used to be a name plus a link column; fold any such pair into one markdown
	// value. Databases that never had the link column fail here with "no such column".
	if _, err := db.Exec(`UPDATE accounts SET slack_channel = '[' || COALESCE(NULLIF(slack_channel, ''), slack_channel_link) || '](' || slack_channel_link || ')',
		slack_channel_link = '' WHERE slack_channel_link != ''`); err != nil && !strings.Contains(err.Error(), "no such column") {
		db.Close()
		return nil, fmt.Errorf("merging slack channel links: %w", err)
	}
	// The column used to be called claude_updated_at. Renaming keeps the stored times; a database
	// that never had it (or was already renamed) answers "no such column".
	if _, err := db.Exec("ALTER TABLE accounts RENAME COLUMN claude_updated_at TO ai_updated_at"); err != nil && !strings.Contains(err.Error(), "no such column") {
		db.Close()
		return nil, fmt.Errorf("renaming column claude_updated_at: %w", err)
	}
	// Columns added after the first release: add them to databases created before that.
	for _, c := range []string{"ai_updated_at", "manual_updated_at"} {
		if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN " + c + " TEXT"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
			db.Close()
			return nil, fmt.Errorf("adding column %s: %w", c, err)
		}
	}
	// The MTM comments as last sent to Salesforce; NULL until the first edit (see sfsync.go).
	if _, err := db.Exec("ALTER TABLE accounts ADD COLUMN mtm_synced_text TEXT"); err != nil && !strings.Contains(err.Error(), "duplicate column") {
		db.Close()
		return nil, fmt.Errorf("adding column mtm_synced_text: %w", err)
	}
	if err := seedSystemBuckets(db); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating built-in buckets: %w", err)
	}
	return db, nil
}

type scanner interface{ Scan(dest ...any) error }

func scanRow(s scanner) (row, error) {
	var r row
	err := s.Scan(&r.ID, &r.Name, &r.Arr, &r.TermEnd, &r.Autorenew, &r.AccountOwner, &r.RenewalStatus, &r.Ews,
		&r.RenewalManager, &r.LastEmail, &r.LastGong, &r.SourceActive, &r.DroppedAt, &r.Risk,
		&r.Cadence, &r.RenewalManagerOverride, &r.ContractOutcome,
		&r.ContractNewArr, &r.ContractNewTermEnd, &r.ContractNewAutorenew, &r.ContractUpdatedAt,
		&r.CreatedAt, &r.UpdatedAt,
		&r.AiUpdatedAt, &r.ManualUpdatedAt,
		&r.AccountLink, &r.AccountOwnerLink, &r.SlackChannel, &r.Details, &r.RenewalManagerLink, &r.MtmComments, &r.MtmSyncedText)
	return r, err
}

func allRows(q querier) ([]row, error) {
	rs, err := q.Query("SELECT " + accountCols + " FROM accounts")
	if err != nil {
		return nil, err
	}
	defer rs.Close()
	var out []row
	for rs.Next() {
		r, err := scanRow(rs)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rs.Err(); err != nil {
		return nil, err
	}
	rs.Close() // release the single connection before the notes query
	notes, err := listNotes(q, "ORDER BY id DESC")
	if err != nil {
		return nil, err
	}
	byAccount := map[int64][]noteItem{}
	for _, n := range notes {
		byAccount[n.AccountID] = append(byAccount[n.AccountID], n)
	}
	for i := range out {
		out[i].Notes = byAccount[out[i].ID]
	}
	return out, nil
}

// noteItem is one timestamped note on an account.
type noteItem struct {
	ID        int64  `json:"id"`
	AccountID int64  `json:"accountId"`
	Text      string `json:"text"`
	CreatedAt string `json:"createdAt"`
}

func listNotes(q querier, clause string, args ...any) ([]noteItem, error) {
	rows, err := q.Query("SELECT id, account_id, body, created_at FROM notes "+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []noteItem{}
	for rows.Next() {
		var n noteItem
		if err := rows.Scan(&n.ID, &n.AccountID, &n.Text, &n.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func allAccounts(q querier, asOf string) ([]Account, error) {
	rows, err := allRows(q)
	if err != nil {
		return nil, err
	}
	out := make([]Account, len(rows))
	for i, r := range rows {
		out[i] = present(r, asOf)
	}
	return out, nil
}

// getRow looks an account up by its numeric id; a missing or malformed id is a 404.
func getRow(q querier, id string) (row, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err == nil && n >= 0 {
		r, err := scanRow(q.QueryRow("SELECT "+accountCols+" FROM accounts WHERE id = ?", n))
		if err == nil {
			r.Notes, err = listNotes(q, "WHERE account_id = ? ORDER BY id DESC", r.ID)
			return r, err
		}
		if err != sql.ErrNoRows {
			return row{}, err
		}
	}
	return row{}, &httpError{404, fmt.Sprintf("Account %s not found", id)}
}

func nameExists(q querier, name string) (bool, error) {
	var one int
	err := q.QueryRow("SELECT 1 FROM accounts WHERE name = ?", name).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

func getSetting(q querier, key string) (*string, error) {
	var v *string
	err := q.QueryRow("SELECT value FROM settings WHERE key = ?", key).Scan(&v)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return v, err
}

func setSetting(q querier, key string, value *string) error {
	if value == nil {
		_, err := q.Exec("DELETE FROM settings WHERE key = ?", key)
		return err
	}
	_, err := q.Exec("INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value", key, *value)
	return err
}

// logActivity records a change. id 0 means a workspace-level event (import, settings).
func logActivity(q querier, id int64, name string, field string, oldV, newV any) error {
	var aid, aname any
	if id != 0 {
		aid, aname = id, name
	}
	_, err := q.Exec("INSERT INTO activity(account_id, account_name, field, old_value, new_value, at) VALUES(?,?,?,?,?,?)",
		aid, aname, field, displayValue(oldV), displayValue(newV), nowISO())
	return err
}

// displayValue renders a column value for the activity log (nil stays NULL).
func displayValue(v any) any {
	switch x := v.(type) {
	case nil:
		return nil
	case string:
		return x
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int64:
		return strconv.FormatInt(x, 10)
	}
	return fmt.Sprint(v)
}

// addNote appends a timestamped note to an account and records it in the activity log.
func addNote(q querier, acct row, body string) error {
	ts := nowISO()
	if _, err := q.Exec("INSERT INTO notes(account_id, body, created_at) VALUES(?,?,?)", acct.ID, body, ts); err != nil {
		return err
	}
	if _, err := q.Exec("UPDATE accounts SET updated_at = ? WHERE id = ?", ts, acct.ID); err != nil {
		return err
	}
	return logActivity(q, acct.ID, acct.Name, "notes", nil, body)
}

// addNotes adds the given notes, skipping any that are already on the account (matched by
// id). The list is newest first, as GET returns it, so it is stored oldest first.
func addNotes(q querier, acct row, list []newNote) error {
	have := map[int64]bool{}
	for _, n := range acct.Notes {
		have[n.ID] = true
	}
	for i := len(list) - 1; i >= 0; i-- {
		if have[list[i].ID] {
			continue
		}
		if err := addNote(q, acct, list[i].Text); err != nil {
			return err
		}
	}
	return nil
}

func inTx(db *sql.DB, fn func(tx *sql.Tx) error) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}
