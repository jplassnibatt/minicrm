package main

// REST API: routing, validation, filtering, sorting.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type httpError struct {
	status int
	msg    string
}

func (e *httpError) Error() string { return e.msg }

func bad(format string, args ...any) error { return &httpError{400, fmt.Sprintf(format, args...)} }

type App struct {
	db     *sql.DB
	apiKey string
}

// ---------------------------------------------------------------- validation
// Validators return a normalised value: string, float64, int64 (0/1 for booleans) or nil.

type validator func(v any, key string) (any, error)

func vText(max int) validator {
	return func(v any, k string) (any, error) {
		if v == nil {
			return "", nil
		}
		s, ok := v.(string)
		if !ok {
			return nil, bad("%s must be a string", k)
		}
		if len([]rune(s)) > max {
			return nil, bad("%s must be at most %d characters", k, max)
		}
		return strings.TrimSpace(s), nil
	}
}

func vName(v any, k string) (any, error) {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil, bad("%s must be a non-empty string", k)
	}
	return strings.TrimSpace(s), nil
}

func vMoney(nullable bool) validator {
	return func(v any, k string) (any, error) {
		if nullable && (v == nil || v == "") {
			return nil, nil
		}
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case string:
			f, err := strconv.ParseFloat(strings.NewReplacer("$", "", ",", "", " ", "").Replace(x), 64)
			if err != nil {
				return nil, bad("%s must be a non-negative number", k)
			}
			n = f
		default:
			return nil, bad("%s must be a non-negative number", k)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || n < 0 {
			return nil, bad("%s must be a non-negative number", k)
		}
		return n, nil
	}
}

func vDate(v any, k string) (any, error) {
	d, ok := parseDate(v)
	if !ok {
		return nil, bad("%s must be a date (YYYY-MM-DD or MM/DD/YYYY) or null", k)
	}
	if d == nil {
		return nil, nil
	}
	return *d, nil
}

// vLink accepts "", an http(s) URL, or a markdown link "[text](https://…)", of which only the
// URL is kept. Anything else (e.g. javascript:) is rejected because the UI renders these as
// clickable links.
func vLink(v any, k string) (any, error) {
	val, err := vText(2000)(v, k)
	if err != nil || val == "" {
		return val, err
	}
	s := val.(string)
	if m := reNameLink.FindStringSubmatch(s); m != nil {
		s = m[2]
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, bad("%s must be a web link starting with http:// or https://, or [text](https://…)", k)
	}
	return s, nil
}

func vTriBool(v any, k string) (any, error) {
	switch v {
	case nil, "":
		return nil, nil
	case true, "Yes", "yes", "true":
		return int64(1), nil
	case false, "No", "no", "false":
		return int64(0), nil
	}
	return nil, bad("%s must be true, false or null", k)
}

func vOneOf(list []string) validator {
	return func(v any, k string) (any, error) {
		if v == nil || v == "" {
			return nil, nil
		}
		if s, ok := v.(string); ok {
			for _, o := range list {
				if o == s {
					return s, nil
				}
			}
		}
		return nil, bad("%s must be one of: %s, or null", k, strings.Join(list, ", "))
	}
}

func validStamp(s string) bool { _, err := time.Parse(time.RFC3339, s); return err == nil }

// newNote is a note supplied in a request. ID is set when the item was copied from a GET
// response; such a note already on the account is not added again.
type newNote struct {
	ID        int64
	Text      string
	CreatedAt string // only used by import, to keep a note's original time
}

// vNotes accepts a single string, or a list of strings or {"text": ...} objects (the shape
// GET returns), so a fetched account can be sent back as is.
func vNotes(v any, k string) (any, error) {
	list, isList := v.([]any)
	if !isList {
		list = []any{v}
	}
	out := []newNote{}
	for _, item := range list {
		var n newNote
		if obj, ok := item.(map[string]any); ok {
			item = obj["text"]
			if id, ok := obj["id"].(float64); ok {
				n.ID = int64(id)
			}
			if c, ok := obj["createdAt"].(string); ok && validStamp(c) {
				n.CreatedAt = c
			}
		}
		text, err := vText(20000)(item, k)
		if err != nil {
			return nil, bad(`%s must be a string, or a list of strings or {"text": "..."} objects`, k)
		}
		if n.Text = text.(string); n.Text != "" {
			out = append(out, n)
		}
	}
	return out, nil
}

// readOnlyFields are computed by the server. They are ignored in request bodies, so an
// account fetched with GET can be sent back unchanged; unknown names are still rejected.
var readOnlyFields = []string{"id", "days", "bucketId", "bucketName", "termType", "fiscalQuarter", "status",
	"inactiveReason", "inactiveSince", "lastEmailDaysAgo", "lastGongDaysAgo", "notesUpdatedAt", "noteCount",
	"contract", "source", "createdAt", "updatedAt"}

type fieldDef struct {
	col      string
	validate validator
}

// API field -> DB column and validator.
var fields = map[string]fieldDef{
	"name":               {"name", vName},
	"arr":                {"arr", vMoney(false)},
	"termEnd":            {"term_end", vDate},
	"autorenew":          {"autorenew", vTriBool},
	"accountOwner":       {"account_owner", vText(200)},
	"accountLink":        {"account_link", vLink},
	"accountOwnerLink":   {"account_owner_link", vLink},
	"renewalManagerLink": {"renewal_manager_link", vLink},
	"slackChannel":       {"slack_channel", vText(2000)},
	// Usually Red/Yellow/Purple/Green, but exports carry others (e.g. "Confirmed Churn").
	"renewalStatus": {"renewal_status", vText(50)},
	// A number. Numeric text ("66", as Salesforce stores it) is accepted; the column stays
	// text so existing databases need no change.
	"ews": {"ews", func(v any, k string) (any, error) {
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
			if err != nil {
				return nil, bad("%s must be a number or null", k)
			}
			v = n
		}
		switch n := v.(type) {
		case nil, string: // null or ""
			return "", nil
		case float64:
			if !math.IsNaN(n) && !math.IsInf(n, 0) {
				return strconv.FormatFloat(n, 'f', -1, 64), nil
			}
		}
		return nil, bad("%s must be a number or null", k)
	}},
	"lastEmail": {"last_email", vDate},
	"lastGong":  {"last_gong", vDate},
	"risk":      {"risk", vOneOf([]string{"Yes", "No"})},
	"cadence":   {"cadence", vOneOf(cadenceOptions)},
	"notes":     {"notes", vNotes},
	"details":   {"details", vText(20000)},
	"renewalManager": {"renewal_manager_override", func(v any, k string) (any, error) {
		s, err := vText(200)(v, k)
		if err != nil || s == "" {
			return nil, err
		}
		return s, nil
	}},
}

var (
	// sourceHeader is the header AI sends on its API writes so MiniCRM can
	// distinguish an AI sync from a human change.
	sourceHeader = "X-MiniCRM-Source"
	sourceClaude = "claude-code"

	// renewalFields are API field names whose change by a non-AI source counts as
	// a manual renewal-field change.
	renewalFields = map[string]bool{
		"arr": true, "termEnd": true, "autorenew": true, "renewalStatus": true, "contract": true,
	}

	allFields = func() []string {
		out := make([]string, 0, len(fields))
		for k := range fields {
			out = append(out, k)
		}
		return out
	}()
)

var reNameLink = regexp.MustCompile(`^\[(.+)\]\((https?://\S+)\)$`)

type fieldVal struct {
	key, col string
	value    any
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// validateFields checks a JSON object against the allowed fields; the result is sorted by key.
func validateFields(body any, allowed []string) ([]fieldVal, error) {
	obj, ok := body.(map[string]any)
	if !ok {
		return nil, bad("Request body must be a JSON object")
	}
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]fieldVal, 0, len(keys))
	for _, k := range keys {
		if contains(readOnlyFields, k) {
			continue
		}
		if !contains(allowed, k) {
			return nil, bad("Unknown field: %s", k)
		}
		def := fields[k]
		v, err := def.validate(obj[k], k)
		if err != nil {
			return nil, err
		}
		out = append(out, fieldVal{k, def.col, v})
	}
	// Like the UI's Account name box: "[Name](https://…)" is the name plus its accountLink.
	if n := findField(out, "name"); n != nil && findField(out, "accountLink") == nil && contains(allowed, "accountLink") {
		if m := reNameLink.FindStringSubmatch(n.value.(string)); m != nil {
			if _, err := vLink(m[2], "accountLink"); err == nil {
				n.value = m[1]
				out = append(out, fieldVal{"accountLink", "account_link", m[2]})
				sort.Slice(out, func(i, j int) bool { return out[i].key < out[j].key })
			}
		}
	}
	return out, nil
}

func findField(f []fieldVal, key string) *fieldVal {
	for i := range f {
		if f[i].key == key {
			return &f[i]
		}
	}
	return nil
}

func ptrStr(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func ptrBool(p *bool) any {
	if p == nil {
		return nil
	}
	if *p {
		return int64(1)
	}
	return int64(0)
}

// col returns the stored value of a writable column, normalised like validator output.
func (r row) col(c string) any {
	switch c {
	case "name":
		return r.Name
	case "arr":
		return r.Arr
	case "term_end":
		return ptrStr(r.TermEnd)
	case "autorenew":
		return ptrBool(r.Autorenew)
	case "account_owner":
		return r.AccountOwner
	case "account_link":
		return r.AccountLink
	case "account_owner_link":
		return r.AccountOwnerLink
	case "slack_channel":
		return r.SlackChannel
	case "renewal_manager_link":
		return r.RenewalManagerLink
	case "details":
		return r.Details
	case "renewal_status":
		return r.RenewalStatus
	case "ews":
		return r.Ews
	case "last_email":
		return ptrStr(r.LastEmail)
	case "last_gong":
		return ptrStr(r.LastGong)
	case "risk":
		return ptrStr(r.Risk)
	case "cadence":
		return ptrStr(r.Cadence)
	case "renewal_manager_override":
		return ptrStr(r.RenewalManagerOverride)
	}
	return nil
}

func sameValue(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	num := func(v any) (float64, bool) {
		switch x := v.(type) {
		case float64:
			return x, true
		case int64:
			return float64(x), true
		}
		return 0, false
	}
	if x, ok := num(a); ok {
		y, ok2 := num(b)
		return ok2 && x == y
	}
	return a == b
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

// hasRenewalFields reports whether any of the given fields is a renewal field (ARR, term
// end, AutoRenew, renewal status, or contract). Used to decide whether a non-AI write
// should stamp manual_updated_at.
func hasRenewalFields(f []fieldVal) bool {
	for _, fv := range f {
		if renewalFields[fv.key] {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------- writes

func (a *App) createAccount(body any, source string) (row, error) {
	f, err := validateFields(body, allFields)
	if err != nil {
		return row{}, err
	}
	name := findField(f, "name")
	if name == nil {
		return row{}, bad("name is required")
	}
	var out row
	err = inTx(a.db, func(tx *sql.Tx) error {
		if exists, err := nameExists(tx, name.value.(string)); err != nil {
			return err
		} else if exists {
			return &httpError{409, fmt.Sprintf("Account %q already exists", name.value)}
		}
		ts := nowISO()
		claude := source == sourceClaude
		cols := []string{"created_at", "updated_at"}
		vals := []any{ts, ts}
		if claude {
			cols = append(cols, "claude_updated_at")
			vals = append(vals, ts)
		} else if hasRenewalFields(f) {
			cols = append(cols, "manual_updated_at")
			vals = append(vals, ts)
		}
		for _, fv := range f {
			if fv.key == "notes" { // stored as a row in the notes table, below
				continue
			}
			cols = append(cols, fv.col)
			vals = append(vals, fv.value)
		}
		res, err := tx.Exec("INSERT INTO accounts("+strings.Join(cols, ",")+") VALUES("+placeholders(len(cols))+")", vals...)
		if err != nil {
			return err
		}
		id, _ := res.LastInsertId()
		if out, err = getRow(tx, strconv.FormatInt(id, 10)); err != nil {
			return err
		}
		if err := logActivity(tx, out.ID, out.Name, "created", nil, out.Name); err != nil {
			return err
		}
		if n := findField(f, "notes"); n != nil {
			if err := addNotes(tx, out, n.value.([]newNote)); err != nil {
				return err
			}
			out, err = getRow(tx, strconv.FormatInt(id, 10))
		}
		return err
	})
	return out, err
}

func (a *App) updateAccount(id string, body any, source string) (row, error) {
	f, err := validateFields(body, allFields)
	if err != nil {
		return row{}, err
	}
	var out row
	err = inTx(a.db, func(tx *sql.Tx) error {
		cur, err := getRow(tx, id)
		if err != nil {
			return err
		}
		out = cur
		var changes []fieldVal
		for _, fv := range f {
			if fv.key == "notes" { // appends notes; never overwrites earlier ones
				if err := addNotes(tx, cur, fv.value.([]newNote)); err != nil {
					return err
				}
			} else if !sameValue(cur.col(fv.col), fv.value) {
				changes = append(changes, fv)
			}
		}
		if len(changes) == 0 {
			out, err = getRow(tx, id)
			return err
		}
		if n := findField(changes, "name"); n != nil {
			if exists, err := nameExists(tx, n.value.(string)); err != nil {
				return err
			} else if exists {
				return &httpError{409, fmt.Sprintf("Account %q already exists", n.value)}
			}
		}
		ts := nowISO()
		claude := source == sourceClaude
		var sets []string
		var vals []any
		for _, c := range changes {
			sets = append(sets, c.col+" = ?")
			vals = append(vals, c.value)
		}
		if claude {
			sets = append(sets, "claude_updated_at = ?")
			vals = append(vals, ts)
		} else if hasRenewalFields(changes) {
			sets = append(sets, "manual_updated_at = ?")
			vals = append(vals, ts)
		}
		vals = append(vals, ts, cur.ID)
		if _, err := tx.Exec("UPDATE accounts SET "+strings.Join(sets, ", ")+", updated_at = ? WHERE id = ?", vals...); err != nil {
			return err
		}
		for _, c := range changes {
			if err := logActivity(tx, cur.ID, cur.Name, c.key, cur.col(c.col), c.value); err != nil {
				return err
			}
		}
		out, err = getRow(tx, id)
		return err
	})
	return out, err
}

func describeContract(outcome, arr, termEnd, autorenew any) any {
	if outcome == nil {
		return nil
	}
	if outcome != "Renewed" {
		return outcome
	}
	or := func(v any) any {
		if v == nil {
			return "unchanged"
		}
		return displayValue(v)
	}
	ar := "unchanged"
	if autorenew != nil {
		ar = "No"
		if autorenew == int64(1) {
			ar = "Yes"
		}
	}
	return fmt.Sprintf("Renewed (ARR %v, term end %v, autorenew %s)", or(arr), or(termEnd), ar)
}

// validateContract checks a contract update; the new values only apply to a renewal.
func validateContract(obj map[string]any) (outcome, newArr, newTermEnd, newAutorenew any, err error) {
	if outcome, err = vOneOf(contractOutcomes)(obj["outcome"], "outcome"); err != nil || outcome != "Renewed" {
		return
	}
	if newArr, err = vMoney(true)(obj["newArr"], "newArr"); err != nil {
		return
	}
	if newTermEnd, err = vDate(obj["newTermEnd"], "newTermEnd"); err != nil {
		return
	}
	newAutorenew, err = vTriBool(obj["newAutorenew"], "newAutorenew")
	return
}

func (a *App) setContract(id string, body any, source string) (row, error) {
	obj, ok := body.(map[string]any)
	if !ok {
		return row{}, bad("Request body must be a JSON object")
	}
	outcome, newArr, newTermEnd, newAutorenew, err := validateContract(obj)
	if err != nil {
		return row{}, err
	}
	var out row
	err = inTx(a.db, func(tx *sql.Tx) error {
		cur, err := getRow(tx, id)
		if err != nil {
			return err
		}
		var oldArr any
		if cur.ContractNewArr != nil {
			oldArr = *cur.ContractNewArr
		}
		before := describeContract(ptrStr(cur.ContractOutcome), oldArr, ptrStr(cur.ContractNewTermEnd), ptrBool(cur.ContractNewAutorenew))
		after := describeContract(outcome, newArr, newTermEnd, newAutorenew)
		if before == after { // nothing changed (e.g. applying "No change" twice): no write, no log entry
			out = cur
			return nil
		}
		ts := nowISO()
		var updatedAt any
		if outcome != nil {
			updatedAt = ts
		}
		claude := source == sourceClaude
		if claude {
			if _, err := tx.Exec(`UPDATE accounts SET contract_outcome = ?, contract_new_arr = ?, contract_new_term_end = ?,
				contract_new_autorenew = ?, contract_updated_at = ?, updated_at = ?, claude_updated_at = ? WHERE id = ?`,
				outcome, newArr, newTermEnd, newAutorenew, updatedAt, ts, ts, cur.ID); err != nil {
				return err
			}
		} else {
			if _, err := tx.Exec(`UPDATE accounts SET contract_outcome = ?, contract_new_arr = ?, contract_new_term_end = ?,
				contract_new_autorenew = ?, contract_updated_at = ?, updated_at = ?, manual_updated_at = ? WHERE id = ?`,
				outcome, newArr, newTermEnd, newAutorenew, updatedAt, ts, ts, cur.ID); err != nil {
				return err
			}
		}
		if err := logActivity(tx, cur.ID, cur.Name, "contract", before, after); err != nil {
			return err
		}
		out, err = getRow(tx, id)
		return err
	})
	return out, err
}

type importResult struct {
	BucketsCreated int `json:"bucketsCreated"`
	Created        int `json:"created"`
	Updated        int `json:"updated"`
	MarkedInactive int `json:"markedInactive"`
	Total          int `json:"total"`
}

// importAccounts bulk-upserts by account name. Only the fields present in the body are
// written, so a plain export of source fields leaves risk, cadence, details, notes and contract
// updates alone, while a file from GET /api/export restores those too. Notes are added, never
// removed. "buckets" in the body adds the rule buckets whose name does not exist yet.
func (a *App) importAccounts(body any, source string) (importResult, error) {
	var res importResult
	list, isArray := body.([]any)
	obj, _ := body.(map[string]any)
	if !isArray {
		list, _ = obj["accounts"].([]any)
	}
	if len(list) == 0 {
		return res, bad(`Body must be { "accounts": [...] } with at least one account`)
	}
	markMissing := true
	if v, ok := obj["markMissingInactive"].(bool); ok {
		markMissing = v
	}
	allowed := allFields
	parsed := make([][]fieldVal, len(list))
	for i, item := range list {
		acct, ok := item.(map[string]any)
		if !ok {
			return res, bad("accounts[%d] must be an object", i)
		}
		picked := map[string]any{}
		for _, k := range allowed {
			if v, ok := acct[k]; ok {
				picked[k] = v
			}
		}
		f, err := validateFields(picked, allowed)
		if err == nil && findField(f, "name") == nil {
			err = bad("name is required")
		}
		if err != nil {
			return res, bad("accounts[%d]: %s", i, err.Error())
		}
		if c, ok := acct["contract"].(map[string]any); ok && err == nil {
			outcome, newArr, newTermEnd, newAutorenew, cerr := validateContract(c)
			var updatedAt any
			if outcome != nil {
				updatedAt = nowISO()
				if s, ok := c["updatedAt"].(string); ok && validStamp(s) {
					updatedAt = s
				}
			}
			err = cerr
			f = append(f, fieldVal{"contract", "contract_outcome", outcome}, fieldVal{"contract", "contract_new_arr", newArr},
				fieldVal{"contract", "contract_new_term_end", newTermEnd}, fieldVal{"contract", "contract_new_autorenew", newAutorenew},
				fieldVal{"contract", "contract_updated_at", updatedAt})
		}
		if err != nil {
			return res, bad("accounts[%d]: %s", i, err.Error())
		}
		// In an import, renewalManager is the source value, not the manual override.
		if rm := findField(f, "renewalManager"); rm != nil {
			rm.col = "renewal_manager"
			if rm.value == nil {
				rm.value = ""
			}
		}
		parsed[i] = f
	}
	var snapshot *string
	_, hasSnapshot := obj["snapshotDate"]
	if hasSnapshot {
		d, ok := parseDate(obj["snapshotDate"])
		if !ok {
			return res, bad("snapshotDate must be a date (YYYY-MM-DD or MM/DD/YYYY) or null")
		}
		snapshot = d
	}

	// Buckets first (each in its own transaction): rule buckets not here yet, appended in file order.
	if list, ok := obj["buckets"].([]any); ok {
		for i, item := range list {
			b, _ := item.(map[string]any)
			name, _ := b["name"].(string)
			if kind, _ := b["kind"].(string); b == nil || (kind != "" && kind != "rule") {
				continue
			}
			if taken, err := bucketNameTaken(a.db, strings.TrimSpace(name), 0); err != nil {
				return res, err
			} else if taken {
				continue
			}
			fresh := map[string]any{}
			for k, v := range b {
				if k != "position" {
					fresh[k] = v
				}
			}
			if _, err := a.createBucket(fresh); err != nil {
				return res, bad("buckets[%d]: %s", i, err.Error())
			}
			res.BucketsCreated++
		}
	}

	res.Total = len(parsed)
	err := inTx(a.db, func(tx *sql.Tx) error {
		ts := nowISO()
		seen := map[string]bool{}
		for _, f := range parsed {
			name := findField(f, "name").value.(string)
			if seen[name] {
				return bad("Duplicate account name in import: %s", name)
			}
			seen[name] = true
			var id int64
			err := tx.QueryRow("SELECT id FROM accounts WHERE name = ?", name).Scan(&id)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
			if err == nil {
				var sets []string
				var vals []any
				for _, fv := range f {
					if fv.key == "notes" { // rows in the notes table, below
						continue
					}
					sets = append(sets, fv.col+" = ?")
					vals = append(vals, fv.value)
				}
				if source == sourceClaude {
					sets = append(sets, "claude_updated_at = ?")
					vals = append(vals, ts)
				} else if hasRenewalFields(f) {
					sets = append(sets, "manual_updated_at = ?")
					vals = append(vals, ts)
				}
				vals = append(vals, ts, id)
				if _, err := tx.Exec("UPDATE accounts SET "+strings.Join(sets, ", ")+", source_active = 1, dropped_at = NULL, updated_at = ? WHERE id = ?", vals...); err != nil {
					return err
				}
				res.Updated++
			} else {
				cols := []string{"created_at", "updated_at"}
				vals := []any{ts, ts}
				if source == sourceClaude {
					cols = append(cols, "claude_updated_at")
					vals = append(vals, ts)
				} else if hasRenewalFields(f) {
					cols = append(cols, "manual_updated_at")
					vals = append(vals, ts)
				}
				for _, fv := range f {
					if fv.key == "notes" {
						continue
					}
					cols = append(cols, fv.col)
					vals = append(vals, fv.value)
				}
				ins, err := tx.Exec("INSERT INTO accounts("+strings.Join(cols, ",")+") VALUES("+placeholders(len(cols))+")", vals...)
				if err != nil {
					return err
				}
				id, _ = ins.LastInsertId()
				res.Created++
			}
			// Notes are newest first in the file; add the ones this account does not have yet,
			// oldest first, keeping their original time when the file gives one.
			if nf := findField(f, "notes"); nf != nil {
				notes := nf.value.([]newNote)
				for i := len(notes) - 1; i >= 0; i-- {
					created := notes[i].CreatedAt
					if _, err := tx.Exec(`INSERT INTO notes(account_id, body, created_at) SELECT ?, ?, ?
						WHERE NOT EXISTS (SELECT 1 FROM notes WHERE account_id = ? AND body = ? AND (? = '' OR created_at = ?))`,
						id, notes[i].Text, firstNonEmpty(created, ts), id, notes[i].Text, created, created); err != nil {
						return err
					}
				}
			}
		}
		if markMissing {
			rows, err := tx.Query("SELECT id, name FROM accounts WHERE source_active = 1")
			if err != nil {
				return err
			}
			var missing []int64
			for rows.Next() {
				var id int64
				var name string
				if err := rows.Scan(&id, &name); err != nil {
					rows.Close()
					return err
				}
				if !seen[name] {
					missing = append(missing, id)
				}
			}
			rows.Close()
			for _, id := range missing {
				if _, err := tx.Exec("UPDATE accounts SET source_active = 0, dropped_at = ?, updated_at = ? WHERE id = ?", ts, ts, id); err != nil {
					return err
				}
			}
			res.MarkedInactive = len(missing)
		}
		if hasSnapshot {
			if err := setSetting(tx, "snapshotDate", snapshot); err != nil {
				return err
			}
		}
		return logActivity(tx, 0, "", "import", nil,
			fmt.Sprintf("%d created, %d updated, %d marked inactive", res.Created, res.Updated, res.MarkedInactive))
	})
	return res, err
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// exportData returns what is needed to recreate the accounts and buckets elsewhere: stored
// values (not the computed ones), every note, the contract update and the rule buckets.
// POST /api/import accepts it as is, and it never marks other accounts inactive.
func (a *App) exportData() (map[string]any, error) {
	rows, err := allRows(a.db)
	if err != nil {
		return nil, err
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].Name) < strings.ToLower(rows[j].Name) })
	accounts := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		p := present(r, todayISO())
		notes := make([]map[string]any, 0, len(r.Notes))
		for _, n := range r.Notes {
			notes = append(notes, map[string]any{"text": n.Text, "createdAt": n.CreatedAt})
		}
		accounts = append(accounts, map[string]any{
			"name": r.Name, "arr": r.Arr, "termEnd": r.TermEnd, "autorenew": r.Autorenew,
			"accountOwner": r.AccountOwner, "accountLink": r.AccountLink, "accountOwnerLink": r.AccountOwnerLink,
			"slackChannel": r.SlackChannel, "renewalStatus": r.RenewalStatus, "ews": p.Ews,
			"renewalManager": p.RenewalManager, "renewalManagerLink": r.RenewalManagerLink,
			"lastEmail": r.LastEmail, "lastGong": r.LastGong, "risk": r.Risk, "cadence": r.Cadence,
			"details": r.Details, "notes": notes, "contract": p.Contract,
		})
	}
	buckets := []bucket{}
	for _, b := range currentBuckets() {
		if b.Kind == "rule" {
			buckets = append(buckets, b)
		}
	}
	return map[string]any{"app": "minicrm", "exportedAt": nowISO(), "markMissingInactive": false,
		"buckets": buckets, "accounts": accounts}, nil
}

// ---------------------------------------------------------------- querying

func csvParam(v string, fn func(string) string) []string {
	var out []string
	for _, s := range strings.Split(v, ",") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, fn(s))
		}
	}
	return out
}

func numParam(q url.Values, k string) (*float64, error) {
	if q.Get(k) == "" {
		return nil, nil
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(q.Get(k)), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return nil, bad("%s must be a number", k)
	}
	return &n, nil
}

func filterAccounts(items []Account, q url.Values) ([]Account, error) {
	lc := strings.ToLower
	status := lc(q.Get("status"))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "inactive" && status != "all" {
		return nil, bad("status must be active, inactive or all")
	}
	wantBuckets := map[int64]bool{}
	for _, v := range csvParam(q.Get("bucketId"), strings.TrimSpace) {
		id, err := strconv.ParseInt(v, 10, 64)
		if _, ok := bucketByID(id); err != nil || !ok {
			return nil, bad("Unknown bucketId: %s", v)
		}
		wantBuckets[id] = true
	}
	owners := csvParam(q.Get("accountOwner"), lc)
	managers := csvParam(q.Get("renewalManager"), lc)
	quarters := csvParam(q.Get("fiscalQuarter"), lc)
	statuses := csvParam(q.Get("renewalStatus"), lc)
	autorenew, risk, hasNotes := lc(q.Get("autorenew")), lc(q.Get("risk")), lc(q.Get("hasNotes"))
	search := strings.TrimSpace(lc(q.Get("q")))
	var minDays, maxDays, minArr, maxArr *float64
	var err error
	for k, dst := range map[string]**float64{"minDays": &minDays, "maxDays": &maxDays, "minArr": &minArr, "maxArr": &maxArr} {
		if *dst, err = numParam(q, k); err != nil {
			return nil, err
		}
	}

	out := make([]Account, 0, len(items))
	for _, a := range items {
		fq := ""
		if a.FiscalQuarter != nil {
			fq = *a.FiscalQuarter
		}
		riskVal := ""
		if a.Risk != nil {
			riskVal = *a.Risk
		}
		noted := a.NoteCount > 0
		switch {
		case status != "all" && lc(a.Status) != status,
			len(wantBuckets) > 0 && !wantBuckets[a.BucketID],
			len(owners) > 0 && !contains(owners, lc(a.AccountOwner)),
			len(managers) > 0 && !contains(managers, lc(a.RenewalManager)),
			len(quarters) > 0 && !contains(quarters, lc(fq)),
			len(statuses) > 0 && !contains(statuses, lc(a.RenewalStatus)),
			autorenew == "yes" && (a.Autorenew == nil || !*a.Autorenew),
			autorenew == "no" && (a.Autorenew == nil || *a.Autorenew),
			risk == "yes" && riskVal != "Yes",
			risk == "no" && riskVal != "No",
			risk == "unset" && riskVal != "",
			minDays != nil && (a.Days == nil || float64(*a.Days) < *minDays),
			maxDays != nil && (a.Days == nil || float64(*a.Days) > *maxDays),
			minArr != nil && a.Arr < *minArr,
			maxArr != nil && a.Arr > *maxArr,
			hasNotes == "true" && !noted,
			hasNotes == "false" && noted,
			search != "" && !strings.Contains(lc(a.Name+" "+a.AccountOwner+" "+a.RenewalManager), search):
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

var sortKeys = []string{"name", "arr", "days", "termEnd", "autorenew", "renewalStatus", "ews", "accountOwner", "renewalManager",
	"engagement", "risk", "cadence", "bucket", "status", "fiscalQuarter", "updatedAt", "notesUpdatedAt"}

// sortVal is a sort key: blank, a number, or a string.
type sortVal struct {
	null  bool
	isNum bool
	num   float64
	str   string
}

func indexOf(list []string, s string) int {
	for i, x := range list {
		if x == s {
			return i
		}
	}
	return -1
}

func sortValue(a Account, key string) sortVal {
	num := func(n float64) sortVal { return sortVal{isNum: true, num: n} }
	str := func(s string) sortVal { return sortVal{null: s == "", str: s} }
	pstr := func(p *string) sortVal {
		if p == nil {
			return sortVal{null: true}
		}
		return str(*p)
	}
	null := sortVal{null: true}
	switch key {
	case "name":
		return str(a.Name)
	case "arr":
		return num(a.Arr)
	case "days":
		if a.Days == nil {
			return null
		}
		return num(float64(*a.Days))
	case "termEnd":
		return pstr(a.TermEnd)
	case "autorenew":
		if a.Autorenew == nil {
			return null
		}
		if *a.Autorenew {
			return num(1)
		}
		return num(0)
	case "renewalStatus":
		return str(a.RenewalStatus)
	case "ews":
		if a.Ews == nil {
			return null
		}
		return num(*a.Ews)
	case "accountOwner":
		return str(a.AccountOwner)
	case "renewalManager":
		return str(a.RenewalManager)
	case "engagement":
		best := math.Inf(1)
		for _, d := range []*int{a.LastEmailDaysAgo, a.LastGongDaysAgo} {
			if d != nil {
				best = math.Min(best, float64(*d))
			}
		}
		if math.IsInf(best, 1) {
			return null
		}
		return num(best)
	case "risk":
		return pstr(a.Risk)
	case "cadence":
		if a.Cadence == nil {
			return null
		}
		return num(float64(indexOf(cadenceOptions, *a.Cadence)))
	case "bucket":
		return num(float64(bucketIndex(a.BucketID)))
	case "status":
		return str(a.Status)
	case "fiscalQuarter":
		return pstr(a.FiscalQuarter)
	case "updatedAt":
		return str(a.UpdatedAt)
	case "notesUpdatedAt":
		return pstr(a.NotesUpdatedAt)
	}
	return null
}

// collate compares case-insensitively, falling back to a plain comparison on ties.
func collate(a, b string) int {
	if c := strings.Compare(strings.ToLower(a), strings.ToLower(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

func sortAccounts(items []Account, q url.Values) ([]Account, error) {
	key := q.Get("sort")
	if key == "" {
		key = "arr"
	}
	if !contains(sortKeys, key) {
		return nil, bad("sort must be one of: %s", strings.Join(sortKeys, ", "))
	}
	order := strings.ToLower(q.Get("order"))
	if order == "" && key == "arr" {
		order = "desc"
	}
	dir := 1
	if order == "desc" {
		dir = -1
	}
	vals := make(map[int64]sortVal, len(items))
	for _, a := range items {
		vals[a.ID] = sortValue(a, key)
	}
	sort.SliceStable(items, func(i, j int) bool {
		x, y := vals[items[i].ID], vals[items[j].ID]
		byName := collate(items[i].Name, items[j].Name) < 0
		switch {
		case x.null && y.null:
			return byName
		case x.null: // blanks always last
			return false
		case y.null:
			return true
		}
		c := 0
		if x.isNum && y.isNum {
			switch {
			case x.num < y.num:
				c = -1
			case x.num > y.num:
				c = 1
			}
		} else {
			c = strings.Compare(strings.ToLower(x.str), strings.ToLower(y.str))
		}
		if c == 0 {
			return byName
		}
		return c*dir < 0
	})
	return items, nil
}

func round2(n float64) float64 { return math.Round(n*100) / 100 }

func sumArr(items []Account) float64 {
	t := 0.0
	for _, a := range items {
		t += a.Arr
	}
	return round2(t)
}

// bucketInfo is a bucket as the API returns it: the stored record plus what is derived from it.
type bucketInfo struct {
	bucket
	Conditions []string `json:"conditions"`
	Rule       string   `json:"rule"`   // the conditions as one line
	System     bool     `json:"system"` // built-in: no conditions, cannot be deleted or reordered
	Count      *int     `json:"count,omitempty"`
	Arr        *float64 `json:"arr,omitempty"`
}

func newBucketInfo(b bucket) bucketInfo {
	cond := b.conditions()
	return bucketInfo{bucket: b, Conditions: cond, Rule: strings.Join(cond, " · "), System: b.Kind != "rule"}
}

func bucketInfos() []bucketInfo {
	all := currentBuckets()
	out := make([]bucketInfo, len(all))
	for i, b := range all {
		out[i] = newBucketInfo(b)
	}
	return out
}

func summary(items []Account, asOf string) map[string]any {
	var active, noAr []Account
	flagged, cadence, notes := 0, 0, 0
	perBucket := map[int64][]Account{}
	for _, a := range items {
		if a.Status != "Active" {
			continue
		}
		active = append(active, a)
		perBucket[a.BucketID] = append(perBucket[a.BucketID], a)
		if a.Risk != nil && *a.Risk == "Yes" {
			flagged++
		}
		if a.Cadence != nil {
			cadence++
		}
		if a.NoteCount > 0 {
			notes++
		}
		if a.Autorenew != nil && !*a.Autorenew && a.Days != nil && *a.Days < 90 {
			noAr = append(noAr, a)
		}
	}
	bs := bucketInfos()
	for i := range bs {
		n, arr := len(perBucket[bs[i].ID]), sumArr(perBucket[bs[i].ID])
		bs[i].Count, bs[i].Arr = &n, &arr
	}
	return map[string]any{
		"asOf": asOf, "total": len(items), "active": len(active), "inactive": len(items) - len(active),
		"activeArr": sumArr(active), "flaggedAtRisk": flagged, "cadenceSet": cadence, "notesAdded": notes,
		"noAutorenewUnder90": map[string]any{"count": len(noAr), "arr": sumArr(noAr)},
		"buckets":            bs,
	}
}

func uniqSorted(list []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range list {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return collate(out[i], out[j]) < 0 })
	return out
}

func (a *App) meta(items []Account, asOf string) (map[string]any, error) {
	// Statuses offered in the UI: the standard four, then any others present in the data
	// (exports carry values such as "Confirmed Churn").
	var owners, managers, otherStatuses []string
	fqs := map[string]int{}
	for _, it := range items {
		owners = append(owners, it.AccountOwner)
		managers = append(managers, it.RenewalManager)
		if st := it.Source.RenewalStatus; !contains(renewalStatuses, st) {
			otherStatuses = append(otherStatuses, st)
		}
		if label, ord, ok := fiscalQuarter(it.TermEnd); ok {
			fqs[label] = ord
		}
	}
	quarters := make([]string, 0, len(fqs))
	for l := range fqs {
		quarters = append(quarters, l)
	}
	sort.Slice(quarters, func(i, j int) bool { return fqs[quarters[i]] < fqs[quarters[j]] })
	snapshot, err := getSetting(a.db, "snapshotDate")
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"version": version, "asOf": asOf, "snapshotDate": snapshot, "today": todayISO(),
		"buckets": bucketInfos(), "cadenceOptions": cadenceOptions, "renewalStatuses": append(append([]string{}, renewalStatuses...), uniqSorted(otherStatuses)...),
		"contractOutcomes": contractOutcomes, "accountOwners": uniqSorted(owners), "renewalManagers": uniqSorted(managers),
		"fiscalQuarters": quarters, "sortKeys": sortKeys,
	}, nil
}

// ---------------------------------------------------------------- HTTP

var endpoints = [][3]string{
	{"GET", "/api/health", "Liveness check (no auth)"},
	{"GET", "/api/meta", "Buckets, option lists, account owners, renewal managers, fiscal quarters, as-of date"},
	{"GET", "/api/summary", "Dashboard counts and per-bucket totals"},
	{"GET", "/api/accounts", "List accounts — filter, sort, paginate"},
	{"POST", "/api/accounts", "Create an account; \"name\" is required and may be \"[Name](https://…)\" to set accountLink too"},
	{"GET", "/api/accounts/:id", "Get one account, including all of its notes"},
	{"PATCH", "/api/accounts/:id", "Update account fields (risk, cadence, details, names and their links, slackChannel, arr, termEnd, ews as a number, …); \"notes\" adds a note"},
	{"DELETE", "/api/accounts/:id", "Delete an account"},
	{"PUT", "/api/accounts/:id/contract", "Record a contract outcome: Renewed / Churned / New CSM / null to clear"},
	{"GET", "/api/accounts/:id/activity", "Change history for one account"},
	{"GET", "/api/accounts/:id/notes", "Notes on one account, newest first"},
	{"POST", "/api/accounts/:id/notes", "Add a timestamped note: { \"text\": \"...\" }"},
	{"DELETE", "/api/accounts/:id/notes/:noteId", "Delete one note"},
	{"GET", "/api/notes", "Every note across all accounts, newest first"},
	{"GET", "/api/activity", "Change history across all accounts"},
	{"GET", "/api/export", "Everything in one JSON file: accounts with notes and contract updates, and the buckets"},
	{"POST", "/api/import", "Bulk upsert accounts by name, from a source export or from a GET /api/export file"},
	{"GET", "/api/buckets", "All buckets with their ids and conditions, in evaluation order"},
	{"POST", "/api/buckets", "Create a bucket: name, conditions, optional color, description and position"},
	{"GET", "/api/buckets/:id", "Get one bucket"},
	{"PATCH", "/api/buckets/:id", "Rename, recolor, reorder or change the conditions of a bucket"},
	{"DELETE", "/api/buckets/:id", "Delete a bucket; its accounts fall through to the next match"},
	{"POST", "/api/buckets/defaults", "Add the standard six buckets (only when none with conditions exist)"},
	{"GET", "/api/keys", "API keys (names and prefixes only) and whether a key is required"},
	{"POST", "/api/keys", "Create an API key: { \"name\": \"...\" }. The key is returned once"},
	{"DELETE", "/api/keys/:id", "Revoke an API key"},
}

var resources = []string{"health", "meta", "summary", "accounts", "notes", "activity", "import", "export", "buckets", "keys"}

const maxBody = 10 << 20

func readBody(w http.ResponseWriter, r *http.Request) (any, error) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return nil, &httpError{413, "Request body too large"}
		}
		return nil, bad("Could not read request body")
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, bad("Request body is not valid JSON")
	}
	return v, nil
}

func sendJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(body); err != nil {
		log.Printf("writing response: %v", err)
	}
}

func paging(q url.Values) (limit, offset int, err error) {
	limit = 100
	if s := q.Get("limit"); s != "" {
		if limit, err = strconv.Atoi(s); err != nil || limit < 1 || limit > 1000 {
			return 0, 0, bad("limit must be an integer between 1 and 1000")
		}
	}
	if s := q.Get("offset"); s != "" {
		if offset, err = strconv.Atoi(s); err != nil || offset < 0 {
			return 0, 0, bad("offset must be a non-negative integer")
		}
	}
	return limit, offset, nil
}

// asOfDate is the date days are counted from: today, unless one request asks for another
// day with ?asOf= (a read-only "what if"; nothing is stored).
func (a *App) asOfDate(q url.Values) (string, error) {
	if s := q.Get("asOf"); s != "" {
		d, ok := parseDate(s)
		if !ok || d == nil {
			return "", bad("asOf must be a date (YYYY-MM-DD)")
		}
		return *d, nil
	}
	return todayISO(), nil
}

func (a *App) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	status, body, err := a.route(w, r)
	if err != nil {
		var he *httpError
		if errors.As(err, &he) {
			sendJSON(w, he.status, map[string]string{"error": he.msg})
			return
		}
		log.Printf("%s %s: %v", r.Method, r.URL.Path, err)
		sendJSON(w, 500, map[string]string{"error": "Internal server error"})
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	sendJSON(w, status, body)
}

type activityItem struct {
	ID          int64   `json:"id"`
	AccountID   *int64  `json:"accountId"`
	AccountName *string `json:"accountName"`
	Field       string  `json:"field"`
	OldValue    *string `json:"oldValue"`
	NewValue    *string `json:"newValue"`
	At          string  `json:"at"`
}

func (a *App) route(w http.ResponseWriter, r *http.Request) (int, any, error) {
	q := r.URL.Query()
	parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api"), "/"), "/")
	if parts[0] == "" {
		parts = nil
	}
	part := func(i int) string {
		if i < len(parts) {
			return parts[i]
		}
		return ""
	}
	resource, id, sub, m := part(0), part(1), part(2), r.Method

	if resource == "health" && m == "GET" {
		return 200, map[string]bool{"ok": true}, nil
	}
	if !a.authorised(r) {
		return 0, nil, &httpError{401, `Missing or invalid API key (send the header "MiniCRM-X-Key: <key>")`}
	}

	switch {
	case resource == "" && m == "GET":
		list := make([]map[string]string, len(endpoints))
		for i, e := range endpoints {
			list[i] = map[string]string{"method": e[0], "path": e[1], "description": e[2]}
		}
		return 200, map[string]any{"name": "MiniCRM API", "endpoints": list}, nil

	case (resource == "meta" || resource == "summary") && m == "GET":
		asOf, err := a.asOfDate(q)
		if err != nil {
			return 0, nil, err
		}
		items, err := allAccounts(a.db, asOf)
		if err != nil {
			return 0, nil, err
		}
		if resource == "summary" {
			return 200, summary(items, asOf), nil
		}
		meta, err := a.meta(items, asOf)
		return 200, meta, err

	case resource == "accounts":
		asOf, err := a.asOfDate(q)
		if err != nil {
			return 0, nil, err
		}
		switch {
		case id == "" && m == "GET":
			limit, offset, err := paging(q)
			if err != nil {
				return 0, nil, err
			}
			items, err := allAccounts(a.db, asOf)
			if err != nil {
				return 0, nil, err
			}
			if items, err = filterAccounts(items, q); err != nil {
				return 0, nil, err
			}
			if items, err = sortAccounts(items, q); err != nil {
				return 0, nil, err
			}
			page := []Account{}
			if offset < len(items) {
				page = items[offset:min(offset+limit, len(items))]
			}
			return 200, map[string]any{"asOf": asOf, "total": len(items), "totalArr": sumArr(items),
				"limit": limit, "offset": offset, "items": page}, nil
		case id == "" && m == "POST":
			body, err := readBody(w, r)
			if err != nil {
				return 0, nil, err
			}
			row, err := a.createAccount(body, r.Header.Get(sourceHeader))
			if err != nil {
				return 0, nil, err
			}
			return 201, present(row, asOf), nil
		case id != "" && sub == "" && m == "GET":
			row, err := getRow(a.db, id)
			if err != nil {
				return 0, nil, err
			}
			return 200, present(row, asOf), nil
		case id != "" && sub == "" && m == "PATCH":
			body, err := readBody(w, r)
			if err != nil {
				return 0, nil, err
			}
			row, err := a.updateAccount(id, body, r.Header.Get(sourceHeader))
			if err != nil {
				return 0, nil, err
			}
			return 200, present(row, asOf), nil
		case id != "" && sub == "" && m == "DELETE":
			err := inTx(a.db, func(tx *sql.Tx) error {
				row, err := getRow(tx, id)
				if err != nil {
					return err
				}
				if _, err := tx.Exec("DELETE FROM accounts WHERE id = ?", row.ID); err != nil {
					return err
				}
				if _, err := tx.Exec("DELETE FROM notes WHERE account_id = ?", row.ID); err != nil {
					return err
				}
				return logActivity(tx, row.ID, row.Name, "deleted", row.Name, nil)
			})
			return 204, nil, err
		case sub == "contract" && m == "PUT":
			body, err := readBody(w, r)
			if err != nil {
				return 0, nil, err
			}
			row, err := a.setContract(id, body, r.Header.Get(sourceHeader))
			if err != nil {
				return 0, nil, err
			}
			return 200, present(row, asOf), nil
		case sub == "notes" && part(3) == "" && m == "GET":
			row, err := getRow(a.db, id)
			if err != nil {
				return 0, nil, err
			}
			return 200, map[string]any{"total": len(row.Notes), "items": present(row, asOf).Notes}, nil
		case sub == "notes" && part(3) == "" && m == "POST":
			body, err := readBody(w, r)
			if err != nil {
				return 0, nil, err
			}
			obj, _ := body.(map[string]any)
			text, err := vText(20000)(obj["text"], "text")
			if err != nil {
				return 0, nil, err
			}
			if text == "" {
				return 0, nil, bad(`Body must be { "text": "..." } with a non-empty note`)
			}
			var row row
			if err := inTx(a.db, func(tx *sql.Tx) error {
				if row, err = getRow(tx, id); err != nil {
					return err
				}
				if err = addNote(tx, row, text.(string)); err != nil {
					return err
				}
				row, err = getRow(tx, id)
				return err
			}); err != nil {
				return 0, nil, err
			}
			return 201, present(row, asOf), nil
		case sub == "notes" && part(3) != "" && m == "DELETE":
			var row row
			err := inTx(a.db, func(tx *sql.Tx) error {
				var err error
				if row, err = getRow(tx, id); err != nil {
					return err
				}
				var body string
				noteID, _ := strconv.ParseInt(part(3), 10, 64)
				err = tx.QueryRow("SELECT body FROM notes WHERE id = ? AND account_id = ?", noteID, row.ID).Scan(&body)
				if err == sql.ErrNoRows {
					return &httpError{404, fmt.Sprintf("Note %s not found on account %s", part(3), id)}
				} else if err != nil {
					return err
				}
				if _, err = tx.Exec("DELETE FROM notes WHERE id = ?", noteID); err != nil {
					return err
				}
				if err = logActivity(tx, row.ID, row.Name, "notes", body, nil); err != nil {
					return err
				}
				row, err = getRow(tx, id)
				return err
			})
			if err != nil {
				return 0, nil, err
			}
			return 200, present(row, asOf), nil
		case sub == "activity" && m == "GET":
			row, err := getRow(a.db, id)
			if err != nil {
				return 0, nil, err
			}
			items, err := a.activity("WHERE account_id = ? ORDER BY id DESC", row.ID)
			return 200, map[string]any{"items": items}, err
		}

	case resource == "notes" && m == "GET":
		asOf, err := a.asOfDate(q)
		if err != nil {
			return 0, nil, err
		}
		items, err := allAccounts(a.db, asOf)
		if err != nil {
			return 0, nil, err
		}
		byID := make(map[int64]Account, len(items))
		for _, it := range items {
			byID[it.ID] = it
		}
		notes, err := listNotes(a.db, "ORDER BY id DESC")
		if err != nil {
			return 0, nil, err
		}
		out := make([]map[string]any, 0, len(notes))
		for _, n := range notes {
			acct := byID[n.AccountID]
			out = append(out, map[string]any{"id": n.ID, "accountId": n.AccountID, "name": acct.Name, "bucketId": acct.BucketID,
				"bucketName": acct.BucketName, "accountOwner": acct.AccountOwner, "status": acct.Status, "note": n.Text, "updatedAt": n.CreatedAt})
		}
		return 200, map[string]any{"total": len(out), "items": out}, nil

	case resource == "activity" && m == "GET":
		limit, offset, err := paging(q)
		if err != nil {
			return 0, nil, err
		}
		var total int
		if err := a.db.QueryRow("SELECT COUNT(*) FROM activity").Scan(&total); err != nil {
			return 0, nil, err
		}
		items, err := a.activity("ORDER BY id DESC LIMIT ? OFFSET ?", limit, offset)
		return 200, map[string]any{"total": total, "limit": limit, "offset": offset, "items": items}, err

	case resource == "export" && m == "GET":
		data, err := a.exportData()
		return 200, data, err

	case resource == "import" && m == "POST":
		body, err := readBody(w, r)
		if err != nil {
			return 0, nil, err
		}
		res, err := a.importAccounts(body, r.Header.Get(sourceHeader))
		return 200, res, err

	case resource == "buckets":
		one := func(bid int64) (int, any, error) {
			b, ok := bucketByID(bid)
			if !ok {
				return 0, nil, &httpError{404, fmt.Sprintf("Bucket %d not found", bid)}
			}
			return 200, newBucketInfo(b), nil
		}
		switch {
		case id == "" && m == "GET":
			return 200, map[string]any{"items": bucketInfos()}, nil
		case id == "" && m == "POST":
			body, err := readBody(w, r)
			if err != nil {
				return 0, nil, err
			}
			bid, err := a.createBucket(body)
			if err != nil {
				return 0, nil, err
			}
			_, b, err := one(bid)
			return 201, b, err
		case id == "defaults" && sub == "" && m == "POST":
			if err := a.addDefaultBuckets(); err != nil {
				return 0, nil, err
			}
			return 201, map[string]any{"items": bucketInfos()}, nil
		case id != "" && sub == "" && (m == "GET" || m == "PATCH" || m == "DELETE"):
			bid, err := strconv.ParseInt(id, 10, 64)
			if err != nil {
				return 0, nil, &httpError{404, fmt.Sprintf("Bucket %s not found", id)}
			}
			switch m {
			case "PATCH":
				body, err := readBody(w, r)
				if err != nil {
					return 0, nil, err
				}
				if err := a.updateBucket(bid, body); err != nil {
					return 0, nil, err
				}
			case "DELETE":
				if err := a.deleteBucket(bid); err != nil {
					return 0, nil, err
				}
				return 204, nil, nil
			}
			return one(bid)
		}

	case resource == "keys" && id == "" && m == "GET":
		items, err := listKeys(a.db)
		return 200, map[string]any{"items": items, "required": a.keyRequired(), "envKey": a.apiKey != ""}, err
	case resource == "keys" && id == "" && m == "POST":
		body, err := readBody(w, r)
		if err != nil {
			return 0, nil, err
		}
		obj, _ := body.(map[string]any)
		name, _ := obj["name"].(string)
		info, key, err := a.createKey(name)
		if err != nil {
			return 0, nil, err
		}
		return 201, map[string]any{"id": info.ID, "name": info.Name, "prefix": info.Prefix, "createdAt": info.CreatedAt, "key": key}, nil
	case resource == "keys" && id != "" && sub == "" && m == "DELETE":
		kid, err := strconv.ParseInt(id, 10, 64)
		if err != nil {
			return 0, nil, &httpError{404, fmt.Sprintf("API key %s not found", id)}
		}
		return 204, nil, a.deleteKey(kid)
	}

	status := 404
	if contains(resources, resource) {
		status = 405
	}
	return 0, nil, &httpError{status, fmt.Sprintf("No route for %s %s", m, r.URL.Path)}
}

func (a *App) activity(clause string, args ...any) ([]activityItem, error) {
	rows, err := a.db.Query("SELECT id, account_id, account_name, field, old_value, new_value, at FROM activity "+clause, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []activityItem{}
	for rows.Next() {
		var it activityItem
		if err := rows.Scan(&it.ID, &it.AccountID, &it.AccountName, &it.Field, &it.OldValue, &it.NewValue, &it.At); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
