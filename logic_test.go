package main

import (
	"database/sql"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// useDefaultBuckets loads the standard scheme plus the two built-in buckets, with ids.
func useDefaultBuckets() {
	all := defaultBuckets()
	for i := range all {
		all[i].ID, all[i].Kind, all[i].Position = int64(i+1), "rule", i+1
	}
	all = append(all, bucket{ID: 7, Kind: "catchall", Name: "Future Watch"}, bucket{ID: 8, Kind: "noterm", Name: "Month-to-Month"})
	setActiveBuckets(all)
}

func TestBucketFor(t *testing.T) {
	useDefaultBuckets()
	yes, no := true, false
	term := "2027-01-01"
	cases := []struct {
		name      string
		arr       float64
		days      int
		autorenew *bool
		want      string
	}{
		{"risk1 lower edge", 25000.01, 30, &no, "First Priority Risk"},
		{"risk1 upper edge", 30000, 60, &no, "First Priority Risk"},
		{"risk2 small arr", 5000, 59, &no, "Second Priority Risk"},
		{"risk2 big arr under 30 days", 90000, 29, &no, "Second Priority Risk"},
		{"risk2 overdue", 90000, -5, &no, "Second Priority Risk"},
		{"risk3", 30000, 61, &no, "Third Priority Risk"},
		{"risk4", 25000, 151, &no, "Fourth Priority Risk"},
		{"no-autorenew falls through to exp2", 20000, 100, &no, "Second Priority Expansion"},
		{"no-autorenew falls through to the catch-all", 5000, 100, &no, "Future Watch"},
		{"exp1", 30000, 60, &yes, "First Priority Expansion"},
		{"exp1 upper edge", 30000, 120, &yes, "First Priority Expansion"},
		{"exp2 big arr outside exp1 window", 30000, 150, &yes, "Second Priority Expansion"},
		{"exp2 mid arr", 10000.01, 30, &yes, "Second Priority Expansion"},
		{"catch-all small arr", 10000, 90, &yes, "Future Watch"},
		{"catch-all far out", 500000, 151, &yes, "Future Watch"},
		{"unknown autorenew treated like yes", 30000, 90, nil, "First Priority Expansion"},
	}
	for _, c := range cases {
		days := c.days
		if got := bucketFor(c.arr, &days, c.autorenew, &term).Name; got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
	if got := bucketFor(50000, nil, &no, nil); got.Kind != "noterm" {
		t.Errorf("no term end: got %q, want the no-term bucket", got.Name)
	}

	// A database with no rule buckets (a fresh install) still places every account.
	setActiveBuckets([]bucket{{ID: 1, Kind: "catchall", Name: "Future Watch"}, {ID: 2, Kind: "noterm", Name: "Month-to-Month"}})
	days := 40
	if got := bucketFor(90000, &days, &no, &term); got.ID != 1 {
		t.Errorf("no rule buckets: got bucket %d, want the catch-all (1)", got.ID)
	}
	if got := bucketFor(90000, nil, &no, nil); got.ID != 2 {
		t.Errorf("no rule buckets, no term: got bucket %d, want 2", got.ID)
	}
	useDefaultBuckets()
}

func TestApplyBucketFields(t *testing.T) {
	b := bucket{Kind: "rule", Autorenew: "any"}
	ok := map[string]any{"name": " Big deals ", "color": "#AABBCC", "autorenew": "no", "arrMin": "$50,000", "daysMin": 10.0, "daysMax": 90.0}
	if err := applyBucketFields(&b, ok); err != nil || b.Name != "Big deals" || b.Color != "#aabbcc" || *b.ArrMin != 50000 || *b.DaysMax != 90 {
		t.Fatalf("valid body: err=%v bucket=%+v", err, b)
	}
	for name, body := range map[string]map[string]any{
		"empty name":      {"name": "  "},
		"bad color":       {"color": "red"},
		"bad autorenew":   {"autorenew": "maybe"},
		"arr range":       {"arrMin": 10.0, "arrMax": 5.0},
		"days range":      {"daysMin": 90.0, "daysMax": 10.0},
		"fractional days": {"daysMin": 1.5},
		"unknown field":   {"nmae": "x"},
	} {
		c := b
		if err := applyBucketFields(&c, body); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	sys := bucket{Kind: "catchall", Name: "Future Watch", Autorenew: "any"}
	if err := applyBucketFields(&sys, map[string]any{"arrMin": 5.0}); err == nil {
		t.Error("built-in bucket: conditions must be rejected")
	}
	if err := applyBucketFields(&sys, map[string]any{"name": "Later", "id": 7.0, "kind": "catchall", "arrMin": nil}); err != nil || sys.Name != "Later" {
		t.Errorf("built-in bucket: rename should work, got %v", err)
	}
}

func TestLink(t *testing.T) {
	for in, ok := range map[string]bool{"": true, "https://example.lightning.force.com/x": true, "http://a.b/c": true,
		"javascript:alert(1)": false, "slack://channel": false, "example.com": false, "https://": false} {
		if _, err := vLink(in, "accountLink"); (err == nil) != ok {
			t.Errorf("vLink(%q): err = %v, want ok = %v", in, err, ok)
		}
	}
	// A markdown link is accepted and only its address is kept.
	if got, err := vLink("[Acme](https://sf.example/001)", "accountLink"); err != nil || got != "https://sf.example/001" {
		t.Errorf("markdown link = %v, %v", got, err)
	}
	if _, err := vLink("[Acme](javascript:alert(1))", "accountLink"); err == nil {
		t.Error("markdown link with a non-web address was accepted")
	}
}

func TestNameLink(t *testing.T) {
	f, err := validateFields(map[string]any{"name": "[Acme](https://sf.example/001)", "arr": 1.0}, allFields)
	if err != nil || findField(f, "name").value != "Acme" || findField(f, "accountLink").value != "https://sf.example/001" {
		t.Errorf("markdown name not split: %v %v", f, err)
	}
	// An explicit accountLink wins, and a non-web link is left as a literal name.
	f, _ = validateFields(map[string]any{"name": "[Acme](https://a.example/1)", "accountLink": ""}, allFields)
	if findField(f, "name").value != "[Acme](https://a.example/1)" {
		t.Errorf("name changed although accountLink was given: %v", f)
	}
	f, _ = validateFields(map[string]any{"name": "[Acme](javascript:x)"}, allFields)
	if findField(f, "accountLink") != nil {
		t.Errorf("non-web link was split out: %v", f)
	}
}

func TestEws(t *testing.T) {
	for _, c := range []struct {
		in   any
		want any // nil = rejected
	}{{66.0, "66"}, {"66", "66"}, {" 39.5 ", "39.5"}, {0.0, "0"}, {nil, ""}, {"", ""}, {"high", nil}, {true, nil}} {
		got, err := fields["ews"].validate(c.in, "ews")
		if (err != nil) != (c.want == nil) || (err == nil && got != c.want) {
			t.Errorf("ews %#v = %#v, %v; want %#v", c.in, got, err, c.want)
		}
	}
	if a := present(row{Ews: "66"}, "2026-01-01"); a.Ews == nil || *a.Ews != 66 {
		t.Errorf("ews 66 presented as %v", a.Ews)
	}
	if a := present(row{}, "2026-01-01"); a.Ews != nil {
		t.Errorf("empty ews presented as %v", *a.Ews)
	}
}

func TestParseDate(t *testing.T) {
	for in, want := range map[string]string{"12/19/2026": "2026-12-19", "2027-2-7": "2027-02-07", " 01/05/2027 ": "2027-01-05"} {
		got, ok := parseDate(in)
		if !ok || got == nil || *got != want {
			t.Errorf("parseDate(%q) = %v, %v; want %s", in, got, ok, want)
		}
	}
	for _, in := range []any{nil, "", "—", "Month-to-Month"} {
		if got, ok := parseDate(in); !ok || got != nil {
			t.Errorf("parseDate(%v) should mean no date", in)
		}
	}
	for _, in := range []any{"2026-02-31", "13/01/2026", "soon", 20261219.0} {
		if _, ok := parseDate(in); ok {
			t.Errorf("parseDate(%v) should be rejected", in)
		}
	}
}

func TestFiscalQuarter(t *testing.T) {
	for in, want := range map[string]string{
		"2026-12-19": "FY2027 Q4", "2027-01-31": "FY2027 Q4", "2027-02-01": "FY2028 Q1",
		"2027-05-16": "FY2028 Q2", "2026-10-31": "FY2027 Q3",
	} {
		d := in
		if got, _, _ := fiscalQuarter(&d); got != want {
			t.Errorf("fiscalQuarter(%s) = %s, want %s", in, got, want)
		}
	}
}

func TestPresentInactiveRule(t *testing.T) {
	useDefaultBuckets()
	term := "2026-10-20"
	base := row{ID: 1, Name: "A", SourceActive: true, TermEnd: &term}
	if a := present(base, "2026-09-28"); a.Status != "Active" { // $0 but renewal in 22 days
		t.Errorf("$0 ARR renewing within 30 days should stay Active, got %s", a.Status)
	}
	if a := present(base, "2026-09-01"); a.Status != "Inactive" { // $0 and 49 days out
		t.Errorf("$0 ARR with no renewal within 30 days should be Inactive, got %s", a.Status)
	}
	renewed, arr, newTerm := "Renewed", 40000.0, "2027-10-20"
	base.ContractOutcome, base.ContractNewArr, base.ContractNewTermEnd = &renewed, &arr, &newTerm
	a := present(base, "2026-09-28")
	if a.Status != "Active" || a.Arr != 40000 || a.RenewalStatus != "Green" || a.BucketName != "Future Watch" {
		t.Errorf("renewal override not applied: %+v", a)
	}
}

func TestPresentNotes(t *testing.T) {
	base := row{ID: 1, Name: "A", Arr: 100, SourceActive: true}
	for _, empty := range [][]noteItem{nil, {}} { // both "no notes" shapes the store can produce
		base.Notes = empty
		a := present(base, "2026-09-28")
		if a.Notes == nil || len(a.Notes) != 0 || a.NoteCount != 0 || a.NotesUpdatedAt != nil {
			t.Errorf("no notes: got notes=%v count=%d updatedAt=%v", a.Notes, a.NoteCount, a.NotesUpdatedAt)
		}
	}
	base.Notes = []noteItem{{ID: 2, Text: "second", CreatedAt: "2026-11-18T21:01:09.801Z"}, {ID: 1, Text: "first", CreatedAt: "2026-11-18T20:00:00.000Z"}}
	a := present(base, "2026-09-28")
	if a.NoteCount != 2 || len(a.Notes) != 2 || a.NotesUpdatedAt == nil || *a.NotesUpdatedAt != "2026-11-18T21:01:09.801Z" {
		t.Errorf("two notes: got count=%d updatedAt=%v", a.NoteCount, a.NotesUpdatedAt)
	}
}

func TestAuthorised(t *testing.T) {
	req := func(header, value string) bool {
		r := httptest.NewRequest("GET", "/api/summary", nil)
		if header != "" {
			r.Header.Set(header, value)
		}
		return (&App{apiKey: "env-key"}).authorised(r)
	}
	defer func() { keyHashes = nil }()

	keyHashes = nil
	if !(&App{}).authorised(httptest.NewRequest("GET", "/api/summary", nil)) {
		t.Error("no env key and no stored keys: the API should be open")
	}
	keyHashes = [][]byte{hashKey("mcrm_stored")}
	if (&App{}).authorised(httptest.NewRequest("GET", "/api/summary", nil)) {
		t.Error("a stored key exists: a request without a key must be refused")
	}
	for name, c := range map[string]struct {
		header, value string
		want          bool
	}{
		"no key":            {"", "", false},
		"wrong key":         {"MiniCRM-X-Key", "nope", false},
		"hash sent as key":  {"MiniCRM-X-Key", string(hashKey("mcrm_stored")), false},
		"env key":           {"MiniCRM-X-Key", "env-key", true},
		"stored key":        {"MiniCRM-X-Key", "mcrm_stored", true},
		"stored key bearer": {"Authorization", "Bearer mcrm_stored", true},
	} {
		if got := req(c.header, c.value); got != c.want {
			t.Errorf("%s: got %v, want %v", name, got, c.want)
		}
	}
}

// An export imported into an empty database must export the same accounts and buckets again.
func TestExportImportRoundTrip(t *testing.T) {
	open := func(name string) *App {
		db, err := openDB(filepath.Join(t.TempDir(), name))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { db.Close() })
		if err := loadBuckets(db); err != nil {
			t.Fatal(err)
		}
		return &App{db: db}
	}
	body := func(s string) any {
		var v any
		if err := json.Unmarshal([]byte(s), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	dump := func(a *App) (string, map[string]any) {
		d, err := a.exportData()
		if err != nil {
			t.Fatal(err)
		}
		delete(d, "exportedAt")
		raw, _ := json.Marshal(d)
		var buckets []map[string]any // ids differ between databases
		json.Unmarshal(raw, &d)
		for _, b := range d["buckets"].([]any) {
			m := b.(map[string]any)
			delete(m, "id")
			buckets = append(buckets, m)
		}
		raw, _ = json.Marshal(d)
		return string(raw), d
	}

	src := open("src.db")
	if _, err := src.createBucket(body(`{"name": "Big and close", "autorenew": "no", "arrMin": 1000, "daysMax": 400}`)); err != nil {
		t.Fatal(err)
	}
	r, err := src.createAccount(body(`{"name": "Acme", "arr": 5000, "termEnd": "2030-01-31", "autorenew": false,
		"accountLink": "https://sf.example/1", "slackChannel": "[#acme](https://slack.example/C1)", "ews": 41,
		"risk": "Yes", "cadence": "Weekly", "details": "line one\nline two", "mtmComments": "02/07/25 first\n11/24/26 second", "notes": ["newest", "oldest"]}`), "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := src.setContract("1", body(`{"outcome": "Renewed", "newArr": 7000, "newTermEnd": "2031-01-31", "newAutorenew": true}`), ""); err != nil {
		t.Fatal(err, r.ID)
	}
	want, data := dump(src)

	dst := open("dst.db")
	if err := loadBuckets(dst.db); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ { // importing twice must not duplicate notes or buckets
		res, err := dst.importAccounts(data, "")
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 && (res.Created != 1 || res.BucketsCreated != 1) || i == 1 && (res.Updated != 1 || res.BucketsCreated != 0) || res.MarkedInactive != 0 {
			t.Errorf("import %d: %+v", i, res)
		}
	}
	if got, _ := dump(dst); got != want {
		t.Errorf("round trip differs:\n got %s\nwant %s", got, want)
	}
}

// A database made before the rename has claude_updated_at; opening it must keep the stored times.
func TestAIUpdatedAtMigration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "old.db")
	old, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.Exec(strings.ReplaceAll(schema, "ai_updated_at", "claude_updated_at")); err != nil {
		t.Fatal(err)
	}
	const stamp = "2026-11-01T10:00:00.000Z"
	if _, err := old.Exec("INSERT INTO accounts(name, created_at, updated_at, claude_updated_at) VALUES('Acme','t','t',?)", stamp); err != nil {
		t.Fatal(err)
	}
	old.Close()

	for i := 0; i < 2; i++ { // the second open must find the column already renamed
		db, err := openDB(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		r, err := getRow(db, "1")
		if err != nil || r.AiUpdatedAt == nil || *r.AiUpdatedAt != stamp {
			t.Errorf("open %d: ai_updated_at %v, %v", i, r.AiUpdatedAt, err)
		}
		if _, err := db.Exec("SELECT claude_updated_at FROM accounts"); err == nil {
			t.Errorf("open %d: the old column is still there", i)
		}
		db.Close()
	}
}

// The source header: "ai" marks a write as the AI's (and "claude-code", still sent by older scripts).
func TestSourceHeaderAI(t *testing.T) {
	useDefaultBuckets()
	db, err := openDB(filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	a := &App{db: db}
	for i, c := range []struct {
		source string
		wantAI bool
	}{{"ai", true}, {"claude-code", true}, {"", false}, {"other", false}} {
		body := `{"name": "Acct` + string(rune('A'+i)) + `", "arr": 100}`
		r, err := a.createAccount(mustJSON(t, body), c.source)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(present(r, "2026-11-24"))
		if got := strings.Contains(string(out), `"aiUpdatedAt"`); got != c.wantAI {
			t.Errorf("source %q: aiUpdatedAt in response = %v, want %v (%s)", c.source, got, c.wantAI, out)
		}
		if strings.Contains(string(out), "claudeUpdatedAt") {
			t.Errorf("source %q: old field name still in the response", c.source)
		}
	}
}

func mustJSON(t *testing.T, s string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(s), &v); err != nil {
		t.Fatal(err)
	}
	return v
}
