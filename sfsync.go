package main

// Appends new MTM comments to Salesforce (Account_Summary__c.MTM_Comments__c) with the
// installed `sf` CLI. MiniCRM keeps the text of the last successful sync in
// accounts.mtm_synced_text; whatever was added after it (or before it, for newest-first
// notes) is the new entry. The entry is added to the value Salesforce holds right now, at the
// same end, so edits made there are never overwritten.
//
// Settings (environment): MINICRM_SF_SYNC=off turns the sync off, MINICRM_SF_ORG is the
// target org (default: the org set with `sf config set target-org`), MINICRM_SF_BIN is the
// path to sf when it is not in a usual place.

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	sfAPIVersion = "v68.0"
	mtmMaxRunes  = 32768 // the Salesforce field limit
)

// mtmSyncResult tells the caller what happened; it is returned with the saved account only.
// Lines are the facts shown in the panel under the box (record id, what sf replied).
type mtmSyncResult struct {
	Status  string   `json:"status"` // synced, skipped, failed or off
	Message string   `json:"message"`
	Lines   []string `json:"lines,omitempty"`
}

func syncResult(status, message string, lines ...string) *mtmSyncResult {
	return &mtmSyncResult{status, message, lines}
}

var (
	sfMu      sync.Mutex // one sf conversation at a time, so two quick edits cannot interleave
	reSFAcct  = regexp.MustCompile(`/r/(?:Account/)?([a-zA-Z0-9]{15,18})/view`)
	reSFIDStr = regexp.MustCompile(`^[a-zA-Z0-9]{15,18}$`)
	reANSI    = regexp.MustCompile(`\x1b\[[0-9;]*m`) // terminal colour codes
	// sfRun runs one sf command and returns its output; tests replace it.
	sfRun = runSF
)

func findSF() string {
	if p := os.Getenv("MINICRM_SF_BIN"); p != "" {
		return p
	}
	// A double-clicked app has a bare PATH, so look in the usual install places too.
	dirs := append(filepath.SplitList(os.Getenv("PATH")), "/usr/local/bin", "/opt/homebrew/bin")
	for _, d := range dirs {
		p := filepath.Join(d, "sf")
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

func runSF(args ...string) ([]byte, error) {
	bin := findSF()
	if bin == "" {
		return nil, errors.New("the sf CLI was not found (install it, or set MINICRM_SF_BIN)")
	}
	if org := os.Getenv("MINICRM_SF_ORG"); org != "" {
		args = append(args, "--target-org", org)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var out, errb bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	// No colour codes in the output we parse, whatever the environment says.
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), errors.New("sf took too long to answer")
		}
		return out.Bytes(), errors.New(sfErrorText(out.Bytes(), errb.Bytes()))
	}
	return out.Bytes(), nil
}

// sfErrorText picks the most useful short message out of a failed sf run.
func sfErrorText(stdout, stderr []byte) string {
	var j struct {
		Message string `json:"message"`
	}
	if decodeSFJSON(stdout, &j) == nil && j.Message != "" {
		return j.Message
	}
	for _, b := range [][]byte{stderr, stdout} {
		if s := strings.TrimSpace(string(b)); s != "" {
			s, _, _ = strings.Cut(s, "\n")
			return s
		}
	}
	return "sf failed"
}

// decodeSFJSON reads the JSON object in sf's output, ignoring any notice sf printed before it
// (for example an update reminder).
func decodeSFJSON(out []byte, v any) error {
	out = reANSI.ReplaceAll(out, nil)
	i := bytes.IndexByte(out, '{')
	if i < 0 {
		return errors.New("no JSON in the output")
	}
	return json.NewDecoder(bytes.NewReader(out[i:])).Decode(v)
}

// sfFindSummary returns the id and current MTM comments of the account's Micro Account Summary.
func sfFindSummary(accountID string) (id, comments string, err error) {
	q := "SELECT Id, MTM_Comments__c FROM Account_Summary__c WHERE Account__c = '" + accountID +
		"' AND RecordType.Name = 'Micro Account Summary'"
	out, err := sfRun("data", "query", "--json", "--query", q)
	if err != nil {
		return "", "", err
	}
	var res struct {
		Result struct {
			Records []struct {
				ID       string `json:"Id"`
				Comments string `json:"MTM_Comments__c"`
			} `json:"records"`
		} `json:"result"`
	}
	if err := decodeSFJSON(out, &res); err != nil {
		head := strings.TrimSpace(string(out))
		if len(head) > 300 {
			head = head[:300]
		}
		log.Printf("MTM sync: unreadable sf answer (%v): %q", err, head)
		return "", "", errors.New("could not read the answer from sf")
	}
	switch n := len(res.Result.Records); {
	case n == 0:
		return "", "", errors.New("Salesforce has no Account Summary record for this account")
	case n > 1:
		return "", "", fmt.Errorf("Salesforce has %d Account Summary records for this account; not sure which to update", n)
	}
	r := res.Result.Records[0]
	if !reSFIDStr.MatchString(r.ID) {
		return "", "", errors.New("unexpected record id from Salesforce")
	}
	return r.ID, r.Comments, nil
}

// sfSetComments replaces MTM_Comments__c on one record and returns the HTTP status line sf
// printed (for example "HTTP/1.1 204"). The body goes through a file so quotes and line
// breaks reach Salesforce exactly as typed.
func sfSetComments(recordID, text string) (string, error) {
	body, _ := json.Marshal(map[string]string{"MTM_Comments__c": text})
	f, err := os.CreateTemp("", "minicrm-mtm-*.json")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(body); err != nil {
		f.Close()
		return "", err
	}
	f.Close()
	// sf exits non-zero on an HTTP error, so a clean exit means Salesforce accepted it.
	out, err := sfRun("api", "request", "rest", "/services/data/"+sfAPIVersion+"/sobjects/Account_Summary__c/"+recordID,
		"-X", "PATCH", "-b", "@"+f.Name(), "-i")
	if err != nil {
		return "", err
	}
	status, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(status), nil
}

func (a *App) setMTMSynced(id int64, text string) {
	if _, err := a.db.Exec("UPDATE accounts SET mtm_synced_text = ? WHERE id = ?", text, id); err != nil {
		log.Printf("saving MTM sync state for account %d: %v", id, err)
	}
}

// syncMTM pushes the MTM comments added since the last sync to Salesforce. It returns nil when
// there is nothing to do. Failures never undo the save in MiniCRM; the unsynced text stays
// pending and goes out with the next edit.
func (a *App) syncMTM(r row) *mtmSyncResult {
	if r.MtmSyncedText == nil || *r.MtmSyncedText == r.MtmComments {
		return nil
	}
	if strings.EqualFold(os.Getenv("MINICRM_SF_SYNC"), "off") {
		return syncResult("off", "Saved. The Salesforce sync is turned off.", "Salesforce sync is turned off (MINICRM_SF_SYNC=off); nothing was sent.")
	}
	base, cur := *r.MtmSyncedText, r.MtmComments
	// The new entry is whatever was added around the text sent last time: after it (oldest
	// first) or before it (newest first). It goes to the same place in the Salesforce text.
	var added string
	atTop := false
	switch {
	case strings.HasPrefix(cur, base):
		added = strings.TrimSpace(cur[len(base):])
	case base != "" && strings.HasSuffix(cur, base):
		added, atTop = strings.TrimSpace(cur[:len(cur)-len(base)]), true
	default:
		// Earlier text changed, so there is no "new entry" to add. Start again from here.
		a.setMTMSynced(r.ID, cur)
		logActivity(a.db, r.ID, r.Name, "mtmSync", nil, "Earlier MTM comments were edited; Salesforce left unchanged")
		return syncResult("skipped", "Saved here, but you changed earlier text, so Salesforce was left unchanged. Update it there by hand; entries added at the top or the end sync again.",
			"Earlier text was changed, so Salesforce was left unchanged.", "New entries added at the top or the end sync again.")
	}
	if added == "" {
		a.setMTMSynced(r.ID, cur)
		return nil
	}
	fail := func(format string, args ...any) *mtmSyncResult {
		msg := fmt.Sprintf(format, args...)
		log.Printf("MTM sync for account %d: %s", r.ID, msg)
		return syncResult("failed", "Saved here, but Salesforce was not updated: "+msg+". It will be sent with your next edit.",
			"Salesforce was not updated: "+msg, "The entry is saved here and will be sent with your next edit.")
	}
	m := reSFAcct.FindStringSubmatch(r.AccountLink)
	if m == nil {
		return fail("this account has no Salesforce link (add it under Account details)")
	}
	sfMu.Lock()
	defer sfMu.Unlock()
	recordID, existing, err := sfFindSummary(m[1])
	if err != nil {
		return fail("%v", err)
	}
	where := "end"
	if atTop {
		where = "top"
	}
	existing = strings.TrimSpace(existing)
	if atTop && strings.HasPrefix(existing, added) || !atTop && strings.HasSuffix(existing, added) {
		a.setMTMSynced(r.ID, cur) // a retry of something Salesforce already has
		return syncResult("synced", "Saved. Salesforce already had this entry.",
			"Record "+recordID+" (Account Summary) already has this entry; nothing was changed.")
	}
	next := added
	switch {
	case existing == "":
	case atTop:
		next = added + "\n" + existing
	default:
		next = existing + "\n" + added
	}
	if n := len([]rune(next)); n > mtmMaxRunes {
		return fail("the Salesforce field would reach %d characters (limit %d)", n, mtmMaxRunes)
	}
	status, err := sfSetComments(recordID, next)
	if err != nil {
		return fail("%v", err)
	}
	a.setMTMSynced(r.ID, cur)
	logActivity(a.db, r.ID, r.Name, "mtmSync", nil, fmt.Sprintf("Updated Salesforce record %s, new entry added at the %s: %s", recordID, where, added))
	lines := []string{"Successfully updated record: " + recordID + ".", "New entry added at the " + where + " of MTM Comments (now " + fmt.Sprint(len([]rune(next))) + " characters)."}
	if status != "" {
		lines = append(lines, "Salesforce replied: "+status)
	}
	return syncResult("synced", "Saved, and the new entry was added to Salesforce.", lines...)
}

// refreshMTM replaces the MTM comments with what Salesforce holds now (the button in the panel).
// Whatever was here is kept in the Activity log.
func (a *App) refreshMTM(id string) (row, *mtmSyncResult, error) {
	r, err := getRow(a.db, id)
	if err != nil {
		return r, nil, err
	}
	if strings.EqualFold(os.Getenv("MINICRM_SF_SYNC"), "off") {
		return r, nil, &httpError{409, "The Salesforce sync is turned off (MINICRM_SF_SYNC=off)"}
	}
	m := reSFAcct.FindStringSubmatch(r.AccountLink)
	if m == nil {
		return r, nil, bad("This account has no Salesforce link (add it under Account details)")
	}
	sfMu.Lock()
	recordID, text, err := sfFindSummary(m[1])
	sfMu.Unlock()
	if err != nil {
		return r, nil, &httpError{502, "Could not read Salesforce: " + err.Error()}
	}
	text = strings.TrimSpace(text)
	if n := len([]rune(text)); n > mtmMaxRunes {
		return r, nil, &httpError{502, fmt.Sprintf("The Salesforce text is %d characters, more than the %d MiniCRM keeps", n, mtmMaxRunes)}
	}
	if text == r.MtmComments && r.MtmSyncedText != nil && *r.MtmSyncedText == text {
		return r, syncResult("synced", "Already up to date with Salesforce.",
			"Successfully read record: "+recordID+".", "The MTM comments here already match Salesforce."), nil
	}
	err = inTx(a.db, func(tx *sql.Tx) error {
		ts := nowISO()
		if _, err := tx.Exec("UPDATE accounts SET mtm_comments = ?, mtm_synced_text = ?, updated_at = ? WHERE id = ?", text, text, ts, r.ID); err != nil {
			return err
		}
		return logActivity(tx, r.ID, r.Name, "mtmComments", r.MtmComments, text)
	})
	if err != nil {
		return r, nil, err
	}
	out, err := getRow(a.db, id)
	return out, syncResult("synced", "MTM comments replaced with the Salesforce text.",
		"Successfully read record: "+recordID+".",
		fmt.Sprintf("MTM comments here replaced with the Salesforce text (%d characters).", len([]rune(text))),
		"The previous text is in the Activity log."), err
}
