package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeSF stands in for the sf CLI: it answers the summary query and records every update.
type fakeSF struct {
	existing   string   // MTM_Comments__c Salesforce holds
	records    int      // how many Account Summary records the query returns
	updates    []string // each text sent to Salesforce
	failUpdate error
}

func (f *fakeSF) run(args ...string) ([]byte, error) {
	switch {
	case args[0] == "data" && args[1] == "query":
		recs := []map[string]any{}
		for i := 0; i < f.records; i++ {
			recs = append(recs, map[string]any{"Id": "aH24W000000PLQtSAO", "MTM_Comments__c": f.existing})
		}
		return json.Marshal(map[string]any{"status": 0, "result": map[string]any{"totalSize": f.records, "records": recs}})
	case args[0] == "api" && args[1] == "request":
		if f.failUpdate != nil {
			return nil, f.failUpdate
		}
		for i, a := range args {
			if a == "-b" {
				raw, err := os.ReadFile(strings.TrimPrefix(args[i+1], "@"))
				if err != nil {
					return nil, err
				}
				var body map[string]string
				json.Unmarshal(raw, &body)
				f.updates = append(f.updates, body["MTM_Comments__c"])
				f.existing = body["MTM_Comments__c"]
			}
		}
		return []byte("HTTP/1.1 204\r\ndate: Tue, 24 Nov 2026 15:00:00 GMT\r\n"), nil
	}
	return nil, errors.New("unexpected sf call")
}

func newSyncApp(t *testing.T, sf *fakeSF) *App {
	t.Helper()
	useDefaultBuckets()
	db, err := openDB(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if sf.records == 0 {
		sf.records = 1
	}
	old := sfRun
	sfRun = sf.run
	t.Cleanup(func() { sfRun = old })
	t.Setenv("MINICRM_SF_SYNC", "")
	return &App{db: db}
}

func jsonBody(t *testing.T, m map[string]string) any {
	t.Helper()
	b, _ := json.Marshal(m)
	var body any
	if err := json.Unmarshal(b, &body); err != nil {
		t.Fatal(err)
	}
	return body
}

// edit sets the MTM comments the way PATCH does, then runs the sync.
func edit(t *testing.T, a *App, text string) *mtmSyncResult {
	t.Helper()
	r, err := a.updateAccount("1", jsonBody(t, map[string]string{"mtmComments": text}), "")
	if err != nil {
		t.Fatal(err)
	}
	return a.syncMTM(r)
}

func newMTMAccount(t *testing.T, a *App, link, text string) {
	t.Helper()
	if _, err := a.createAccount(jsonBody(t, map[string]string{"name": "Luma", "accountLink": link, "mtmComments": text}), ""); err != nil {
		t.Fatal(err)
	}
}

const lumaLink = "[Luma](https://pagerduty.lightning.force.com/lightning/r/0014W00002OM7TCQA1/view)"

func TestMTMSyncAppendsNewEntry(t *testing.T) {
	sf := &fakeSF{existing: "02/07/25 old"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "02/07/25 old")

	res := edit(t, a, "02/07/25 old\n11/24/26 new")
	if res == nil || res.Status != "synced" {
		t.Fatalf("result %+v", res)
	}
	if len(sf.updates) != 1 || sf.updates[0] != "02/07/25 old\n11/24/26 new" {
		t.Errorf("sent %q", sf.updates)
	}
	// The panel shows the record, where the entry went and what sf replied.
	for _, want := range []string{"Successfully updated record: aH24W000000PLQtSAO.", "added at the end", "HTTP/1.1 204"} {
		if !strings.Contains(strings.Join(res.Lines, "\n"), want) {
			t.Errorf("lines %q lack %q", res.Lines, want)
		}
	}
	// A second entry goes out as its own append.
	edit(t, a, "02/07/25 old\n11/24/26 new\n11/25/26 more")
	if len(sf.updates) != 2 || sf.updates[1] != "02/07/25 old\n11/24/26 new\n11/25/26 more" {
		t.Errorf("sent %q", sf.updates)
	}
}

func TestMTMSyncToleratesColouredSFJSON(t *testing.T) {
	sf := &fakeSF{existing: "A"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	sfRun = func(args ...string) ([]byte, error) {
		out, err := sf.run(args...)
		if args[0] == "data" { // colour every brace, key and string the way sf does on a colour terminal
			s := strings.NewReplacer("{", "\x1b[97m{\x1b[39m", `"status"`, "\x1b[94m\"status\"\x1b[39m", `"A"`, "\x1b[92m\"A\"\x1b[39m").Replace(string(out))
			out = []byte(s)
		}
		return out, err
	}
	if res := edit(t, a, "A\nB"); res == nil || res.Status != "synced" {
		t.Errorf("result %+v", res)
	}
}

func TestMTMSyncToleratesNoticesBeforeSFJSON(t *testing.T) {
	sf := &fakeSF{existing: "A"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	sfRun = func(args ...string) ([]byte, error) {
		out, err := sf.run(args...)
		if args[0] == "data" {
			out = append([]byte(" ›   Warning: @salesforce/cli update available from 2.159.7 to 2.160.0.\n"), out...)
		}
		return out, err
	}
	if res := edit(t, a, "A\nB"); res == nil || res.Status != "synced" {
		t.Errorf("result %+v", res)
	}
}

func TestMTMSyncKeepsEditsMadeInSalesforce(t *testing.T) {
	sf := &fakeSF{existing: "02/07/25 old\n11/20/26 added by someone in Salesforce"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "02/07/25 old")

	edit(t, a, "02/07/25 old\n11/24/26 new")
	want := "02/07/25 old\n11/20/26 added by someone in Salesforce\n11/24/26 new"
	if len(sf.updates) != 1 || sf.updates[0] != want {
		t.Errorf("sent %q, want %q", sf.updates, want)
	}
}

func TestMTMSyncNewestFirstGoesToTheTop(t *testing.T) {
	sf := &fakeSF{existing: "02/07/25 old\n11/20/26 added in Salesforce"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "02/07/25 old")

	res := edit(t, a, "11/24/26 new\n02/07/25 old")
	if res == nil || res.Status != "synced" {
		t.Fatalf("result %+v", res)
	}
	want := "11/24/26 new\n02/07/25 old\n11/20/26 added in Salesforce"
	if len(sf.updates) != 1 || sf.updates[0] != want {
		t.Errorf("sent %q, want %q", sf.updates, want)
	}
	// A second entry on top goes above the first.
	edit(t, a, "11/25/26 newer\n11/24/26 new\n02/07/25 old")
	if len(sf.updates) != 2 || !strings.HasPrefix(sf.updates[1], "11/25/26 newer\n11/24/26 new\n") {
		t.Errorf("sent %q", sf.updates)
	}
}

func TestMTMSyncNewestFirstOnEmptyBaseAndRepeat(t *testing.T) {
	sf := &fakeSF{existing: "11/24/26 new\n02/07/25 old"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "02/07/25 old")
	// Salesforce already starts with this entry: nothing is sent.
	res := edit(t, a, "11/24/26 new\n02/07/25 old")
	if res == nil || res.Status != "synced" || len(sf.updates) != 0 {
		t.Errorf("result %+v, updates %q", res, sf.updates)
	}
}

func TestMTMSyncEmptySalesforceField(t *testing.T) {
	sf := &fakeSF{}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "")
	edit(t, a, "11/24/26 first")
	if len(sf.updates) != 1 || sf.updates[0] != "11/24/26 first" {
		t.Errorf("sent %q", sf.updates)
	}
}

func TestMTMSyncEditedEarlierTextLeavesSalesforceAlone(t *testing.T) {
	sf := &fakeSF{existing: "02/07/25 old"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "02/07/25 old")

	res := edit(t, a, "02/07/25 CHANGED")
	if res == nil || res.Status != "skipped" || len(sf.updates) != 0 {
		t.Fatalf("result %+v, updates %q", res, sf.updates)
	}
	// Appending after that works again, from the new text.
	if res := edit(t, a, "02/07/25 CHANGED\n11/24/26 new"); res == nil || res.Status != "synced" {
		t.Fatalf("result %+v", res)
	}
	if len(sf.updates) != 1 || sf.updates[0] != "02/07/25 old\n11/24/26 new" {
		t.Errorf("sent %q", sf.updates)
	}
}

func TestMTMSyncFailureIsRetriedWithNextEdit(t *testing.T) {
	sf := &fakeSF{existing: "A", failUpdate: errors.New("INVALID_SESSION_ID")}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")

	res := edit(t, a, "A\nB")
	if res == nil || res.Status != "failed" || !strings.Contains(res.Message, "INVALID_SESSION_ID") {
		t.Fatalf("result %+v", res)
	}
	got, _ := getRow(a.db, "1")
	if got.MtmComments != "A\nB" {
		t.Errorf("the save in MiniCRM must stand, got %q", got.MtmComments)
	}
	sf.failUpdate = nil
	edit(t, a, "A\nB\nC")
	if len(sf.updates) != 1 || sf.updates[0] != "A\nB\nC" {
		t.Errorf("sent %q, want the missed entry B sent together with C", sf.updates)
	}
}

func TestMTMSyncDoesNotRepeatAnEntrySalesforceAlreadyHas(t *testing.T) {
	sf := &fakeSF{existing: "A\nB"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	res := edit(t, a, "A\nB")
	if res == nil || res.Status != "synced" || len(sf.updates) != 0 {
		t.Errorf("result %+v, updates %q", res, sf.updates)
	}
}

func TestMTMSyncProblems(t *testing.T) {
	cases := []struct {
		name    string
		link    string
		sf      *fakeSF
		noQuery bool // sf finds no Account Summary record
		want    string
	}{
		{"no Salesforce link", "", &fakeSF{}, false, "no Salesforce link"},
		{"no summary record", lumaLink, &fakeSF{}, true, "no Account Summary record"},
		{"two summary records", lumaLink, &fakeSF{records: 2}, false, "2 Account Summary records"},
		{"too long", lumaLink, &fakeSF{existing: strings.Repeat("x", 32760)}, false, "limit 32768"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := newSyncApp(t, c.sf)
			if c.noQuery {
				sfRun = func(args ...string) ([]byte, error) {
					return []byte(`{"status":0,"result":{"totalSize":0,"records":[]}}`), nil
				}
			}
			newMTMAccount(t, a, c.link, "")
			res := edit(t, a, "11/24/26 entry long enough")
			if res == nil || res.Status != "failed" || !strings.Contains(res.Message, c.want) || len(c.sf.updates) != 0 {
				t.Errorf("result %+v, updates %q", res, c.sf.updates)
			}
		})
	}
}

func TestMTMSyncCanBeTurnedOff(t *testing.T) {
	sf := &fakeSF{existing: "A"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	t.Setenv("MINICRM_SF_SYNC", "off")
	res := edit(t, a, "A\nB")
	if res == nil || res.Status != "off" || len(sf.updates) != 0 {
		t.Fatalf("result %+v, updates %q", res, sf.updates)
	}
	// Turned on again, the entry made meanwhile goes out.
	t.Setenv("MINICRM_SF_SYNC", "")
	edit(t, a, "A\nB\nC")
	if len(sf.updates) != 1 || sf.updates[0] != "A\nB\nC" {
		t.Errorf("sent %q", sf.updates)
	}
}

func TestMTMSyncSkippedForWritesFromTheAI(t *testing.T) {
	sf := &fakeSF{existing: "A\nB"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	srv := httptest.NewServer(a)
	defer srv.Close()
	req, _ := http.NewRequest("PATCH", srv.URL+"/api/accounts/1", strings.NewReader(`{"mtmComments": "A\nB"}`))
	req.Header.Set(sourceHeader, sourceAI)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MtmSync *mtmSyncResult `json:"mtmSync"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	row, _ := getRow(a.db, "1")
	if got.MtmSync != nil || len(sf.updates) != 0 || row.MtmSyncedText == nil || *row.MtmSyncedText != "A\nB" {
		t.Errorf("mtmSync %+v, updates %q, synced %v", got.MtmSync, sf.updates, row.MtmSyncedText)
	}
}

func TestMTMRefreshReplacesTheLocalText(t *testing.T) {
	sf := &fakeSF{existing: "02/07/25 old\n11/24/26 from Salesforce"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "local only text")

	r, res, err := a.refreshMTM("1")
	if err != nil {
		t.Fatal(err)
	}
	if r.MtmComments != "02/07/25 old\n11/24/26 from Salesforce" || r.MtmSyncedText == nil || *r.MtmSyncedText != r.MtmComments {
		t.Errorf("text %q, synced %v", r.MtmComments, r.MtmSyncedText)
	}
	if res == nil || res.Status != "synced" || !strings.Contains(strings.Join(res.Lines, "\n"), "Successfully read record: aH24W000000PLQtSAO.") {
		t.Errorf("result %+v", res)
	}
	if len(sf.updates) != 0 {
		t.Errorf("a refresh must never write to Salesforce, sent %q", sf.updates)
	}
	acts, _ := a.activity("WHERE account_id = ? ORDER BY id DESC", 1)
	if acts[0].Field != "mtmComments" || acts[0].OldValue == nil || *acts[0].OldValue != "local only text" {
		t.Errorf("the replaced text must be kept in Activity: %+v", acts[0])
	}
	// Refreshing again changes nothing.
	if _, res, err := a.refreshMTM("1"); err != nil || res.Message != "Already up to date with Salesforce." {
		t.Errorf("second refresh: %+v, %v", res, err)
	}
}

func TestMTMRefreshProblems(t *testing.T) {
	sf := &fakeSF{existing: "x"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, "", "local")
	if _, _, err := a.refreshMTM("1"); err == nil || !strings.Contains(err.Error(), "no Salesforce link") {
		t.Errorf("no link: %v", err)
	}
	if _, _, err := a.refreshMTM("99"); err == nil {
		t.Errorf("unknown account must fail")
	}
	b := newSyncApp(t, &fakeSF{})
	newMTMAccount(t, b, lumaLink, "local")
	sfRun = func(args ...string) ([]byte, error) { return nil, errors.New("INVALID_SESSION_ID") }
	if _, _, err := b.refreshMTM("1"); err == nil || !strings.Contains(err.Error(), "INVALID_SESSION_ID") {
		t.Errorf("sf error: %v", err)
	}
	got, _ := getRow(b.db, "1")
	if got.MtmComments != "local" {
		t.Errorf("a failed refresh must leave the text alone, got %q", got.MtmComments)
	}
	t.Setenv("MINICRM_SF_SYNC", "off")
	if _, _, err := b.refreshMTM("1"); err == nil || !strings.Contains(err.Error(), "turned off") {
		t.Errorf("off: %v", err)
	}
}

func TestMTMRefreshRoute(t *testing.T) {
	sf := &fakeSF{existing: "from Salesforce"}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "local")
	srv := httptest.NewServer(a)
	defer srv.Close()
	resp, err := http.Post(srv.URL+"/api/accounts/1/mtm-refresh", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got struct {
		MtmComments string         `json:"mtmComments"`
		MtmSync     *mtmSyncResult `json:"mtmSync"`
	}
	json.NewDecoder(resp.Body).Decode(&got)
	resp.Body.Close()
	if resp.StatusCode != 200 || got.MtmComments != "from Salesforce" || got.MtmSync == nil || len(got.MtmSync.Lines) == 0 {
		t.Errorf("status %d, %+v", resp.StatusCode, got)
	}
}

func TestMTMSyncIgnoresOtherEdits(t *testing.T) {
	sf := &fakeSF{}
	a := newSyncApp(t, sf)
	newMTMAccount(t, a, lumaLink, "A")
	r, err := a.updateAccount("1", jsonBody(t, map[string]string{"risk": "Yes"}), "")
	if err != nil {
		t.Fatal(err)
	}
	if res := a.syncMTM(r); res != nil || len(sf.updates) != 0 {
		t.Errorf("result %+v, updates %q", res, sf.updates)
	}
}
