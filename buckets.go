package main

// Buckets are records in the database, identified by a numeric id.
//
// A "rule" bucket has conditions (AutoRenew, ARR range, days range). An account goes into
// the first rule bucket, in position order, whose conditions it meets. Two built-in buckets
// always exist and have no conditions: "noterm" takes every account without a term end
// date, and "catchall" takes whatever no rule bucket claimed. A new database has only those
// two; rule buckets are added by the user (or with the standard set, see defaultBuckets).

import (
	"database/sql"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type bucket struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Color       string   `json:"color"`
	Kind        string   `json:"kind"`      // "rule", "catchall" or "noterm"
	Position    int      `json:"position"`  // order among rule buckets, from 1
	Autorenew   string   `json:"autorenew"` // "no", "yes" or "any"
	ArrMin      *float64 `json:"arrMin"`    // ARR must be greater than this
	ArrMax      *float64 `json:"arrMax"`    // ARR must be at most this
	DaysMin     *int     `json:"daysMin"`   // days to renewal must be at least this
	DaysMax     *int     `json:"daysMax"`   // days to renewal must be at most this
}

func fp(v float64) *float64 { return &v }
func ip(v int) *int         { return &v }

// defaultBuckets is the standard scheme from the original export. It is only added on
// request (POST /api/buckets/defaults).
func defaultBuckets() []bucket {
	return []bucket{
		{Name: "First Priority Risk", Color: "#c9372c", Autorenew: "no", ArrMin: fp(25000), DaysMin: ip(30), DaysMax: ip(60),
			Description: "The core renewal-risk watchlist — big accounts, no auto-renew, actionable window."},
		{Name: "Second Priority Risk", Color: "#de6a1c", Autorenew: "no", DaysMax: ip(59),
			Description: "Everything else renewing soon without auto-renew — smaller accounts, or large ones inside 30 days."},
		{Name: "Third Priority Risk", Color: "#d99a16", Autorenew: "no", ArrMin: fp(25000), DaysMin: ip(61),
			Description: "Larger runway, still no auto-renew — worth tracking, lower urgency this week."},
		{Name: "Fourth Priority Risk", Color: "#b8a44a", Autorenew: "no", ArrMax: fp(25000), DaysMin: ip(151),
			Description: "Small accounts that won't auto-renew, still a long way out. Low urgency, but tracked."},
		{Name: "First Priority Expansion", Color: "#05944f", Autorenew: "any", ArrMin: fp(25000), DaysMin: ip(60), DaysMax: ip(120),
			Description: "Healthy, sizeable accounts approaching renewal — good upsell and expansion timing."},
		{Name: "Second Priority Expansion", Color: "#0b9a94", Autorenew: "any", ArrMin: fp(10000), DaysMin: ip(30), DaysMax: ip(150),
			Description: "Broader expansion pool — lower ARR floor, wider window, everything not already claimed above."},
	}
}

const bucketCols = "id, name, description, color, kind, position, autorenew, arr_min, arr_max, days_min, days_max"

// bucketOrder lists rule buckets by position, then the catch-all, then the no-term bucket.
const bucketOrder = " ORDER BY CASE kind WHEN 'rule' THEN 0 WHEN 'catchall' THEN 1 ELSE 2 END, position, id"

// seedSystemBuckets makes sure the two built-in buckets exist.
func seedSystemBuckets(q querier) error {
	for _, b := range []bucket{
		{Kind: "catchall", Name: "Future Watch", Color: "#3c6fd6",
			Description: "Everything with a known term date not claimed by a bucket above. Scan periodically rather than weekly."},
		{Kind: "noterm", Name: "Month-to-Month", Color: "#7c8594",
			Description: "Accounts with no subscription term tracked — billed month-to-month rather than on a fixed renewal date."},
	} {
		if _, err := q.Exec(`INSERT INTO buckets(name, description, color, kind, position)
			SELECT ?, ?, ?, ?, 0 WHERE NOT EXISTS (SELECT 1 FROM buckets WHERE kind = ?)`,
			b.Name, b.Description, b.Color, b.Kind, b.Kind); err != nil {
			return err
		}
	}
	return nil
}

func listBuckets(q querier) ([]bucket, error) {
	rows, err := q.Query("SELECT " + bucketCols + " FROM buckets" + bucketOrder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []bucket{}
	for rows.Next() {
		var b bucket
		if err := rows.Scan(&b.ID, &b.Name, &b.Description, &b.Color, &b.Kind, &b.Position, &b.Autorenew,
			&b.ArrMin, &b.ArrMax, &b.DaysMin, &b.DaysMax); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// The bucket list is read on every request, so it is cached and refreshed after each change.
var (
	bucketsMu     sync.RWMutex
	activeBuckets []bucket
)

func currentBuckets() []bucket {
	bucketsMu.RLock()
	defer bucketsMu.RUnlock()
	return activeBuckets
}

func setActiveBuckets(b []bucket) {
	bucketsMu.Lock()
	activeBuckets = b
	bucketsMu.Unlock()
}

func loadBuckets(q querier) error {
	b, err := listBuckets(q)
	if err == nil {
		setActiveBuckets(b)
	}
	return err
}

func bucketByID(id int64) (bucket, bool) {
	for _, b := range currentBuckets() {
		if b.ID == id {
			return b, true
		}
	}
	return bucket{}, false
}

func bucketIndex(id int64) int {
	for i, b := range currentBuckets() {
		if b.ID == id {
			return i
		}
	}
	return -1
}

func (b bucket) matches(arr float64, days int, autorenew *bool) bool {
	switch b.Autorenew {
	case "no":
		if autorenew == nil || *autorenew {
			return false
		}
	case "yes":
		if autorenew == nil || !*autorenew {
			return false
		}
	}
	return (b.ArrMin == nil || arr > *b.ArrMin) &&
		(b.ArrMax == nil || arr <= *b.ArrMax) &&
		(b.DaysMin == nil || days >= *b.DaysMin) &&
		(b.DaysMax == nil || days <= *b.DaysMax)
}

// bucketFor returns the bucket an account belongs to.
func bucketFor(arr float64, days *int, autorenew *bool, termEnd *string) bucket {
	var catchall, noterm bucket
	all := currentBuckets()
	for _, b := range all {
		switch b.Kind {
		case "catchall":
			catchall = b
		case "noterm":
			noterm = b
		}
	}
	if termEnd == nil {
		return noterm
	}
	if days != nil {
		for _, b := range all {
			if b.Kind == "rule" && b.matches(arr, *days, autorenew) {
				return b
			}
		}
	}
	return catchall
}

func shortMoney(n float64) string {
	trim := func(f float64) string { return strconv.FormatFloat(math.Round(f*100)/100, 'f', -1, 64) }
	switch {
	case n >= 1e6:
		return "$" + trim(n/1e6) + "M"
	case n >= 1e3:
		return "$" + trim(n/1e3) + "K"
	}
	return "$" + trim(n)
}

// conditions renders a bucket's rule the way section headers read:
// "AutoRenew = No", "ARR > $25K", "30–60 days".
func (b bucket) conditions() []string {
	switch b.Kind {
	case "catchall":
		return []string{"Catch-all", "Known term end date", "Not claimed by a bucket above"}
	case "noterm":
		return []string{"No term end date on file"}
	}
	out := make([]string, 0, 3)
	switch b.Autorenew {
	case "no":
		out = append(out, "AutoRenew = No")
	case "yes":
		out = append(out, "AutoRenew = Yes")
	default:
		out = append(out, "Any AutoRenew")
	}
	switch {
	case b.ArrMin != nil && b.ArrMax != nil:
		out = append(out, fmt.Sprintf("ARR > %s up to %s", shortMoney(*b.ArrMin), shortMoney(*b.ArrMax)))
	case b.ArrMin != nil:
		out = append(out, "ARR > "+shortMoney(*b.ArrMin))
	case b.ArrMax != nil:
		out = append(out, "ARR ≤ "+shortMoney(*b.ArrMax))
	default:
		out = append(out, "Any ARR")
	}
	switch {
	case b.DaysMin != nil && b.DaysMax != nil:
		out = append(out, fmt.Sprintf("%d–%d days", *b.DaysMin, *b.DaysMax))
	case b.DaysMin != nil:
		out = append(out, fmt.Sprintf(">%d days", *b.DaysMin-1))
	case b.DaysMax != nil:
		out = append(out, fmt.Sprintf("<%d days", *b.DaysMax+1))
	default:
		out = append(out, "Any renewal date")
	}
	return out
}

func (b bucket) summaryLine() string { return b.Name + ": " + strings.Join(b.conditions(), " · ") }

var reColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Keys a client may send back from a GET; they are ignored.
var bucketReadOnly = []string{"id", "kind", "conditions", "rule", "system", "count", "arr"}

// applyBucketFields validates a request body and applies it to b. On a built-in bucket
// only name, description and color may change.
func applyBucketFields(b *bucket, body any) error {
	obj, ok := body.(map[string]any)
	if !ok {
		return bad("Request body must be a JSON object")
	}
	number := func(k string, v any, whole bool) (*float64, error) {
		if v == nil || v == "" {
			return nil, nil
		}
		var n float64
		switch x := v.(type) {
		case float64:
			n = x
		case string:
			f, err := strconv.ParseFloat(strings.NewReplacer("$", "", ",", "", " ", "").Replace(x), 64)
			if err != nil {
				return nil, bad("%s must be a number or null", k)
			}
			n = f
		default:
			return nil, bad("%s must be a number or null", k)
		}
		if math.IsNaN(n) || math.IsInf(n, 0) || (whole && n != math.Trunc(n)) || (!whole && n < 0) {
			if whole {
				return nil, bad("%s must be a whole number or null", k)
			}
			return nil, bad("%s must be a non-negative number or null", k)
		}
		return &n, nil
	}
	for k, v := range obj {
		if contains(bucketReadOnly, k) {
			continue
		}
		if b.Kind != "rule" && k != "name" && k != "description" && k != "color" {
			if contains([]string{"autorenew", "arrMin", "arrMax", "daysMin", "daysMax", "position"}, k) {
				// Sending a built-in bucket back unchanged is fine; changing these is not.
				if v == nil || v == "any" || v == float64(b.Position) {
					continue
				}
				return bad("%q is a built-in bucket: only its name, description and color can change", b.Name)
			}
		}
		var err error
		switch k {
		case "name":
			s, _ := v.(string)
			if s = strings.TrimSpace(s); s == "" || len([]rune(s)) > 100 {
				return bad("name must be 1 to 100 characters")
			}
			b.Name = s
		case "description":
			s, ok := v.(string)
			if (!ok && v != nil) || len([]rune(s)) > 500 {
				return bad("description must be a string of at most 500 characters")
			}
			b.Description = strings.TrimSpace(s)
		case "color":
			s, _ := v.(string)
			if !reColor.MatchString(s) {
				return bad(`color must look like "#1a2b3c"`)
			}
			b.Color = strings.ToLower(s)
		case "autorenew":
			s, _ := v.(string)
			if s = strings.ToLower(s); s != "any" && s != "no" && s != "yes" {
				return bad("autorenew must be any, no or yes")
			}
			b.Autorenew = s
		case "arrMin":
			b.ArrMin, err = number(k, v, false)
		case "arrMax":
			b.ArrMax, err = number(k, v, false)
		case "daysMin", "daysMax", "position":
			var n *float64
			if n, err = number(k, v, true); err == nil {
				var p *int
				if n != nil {
					p = ip(int(*n))
				}
				switch k {
				case "daysMin":
					b.DaysMin = p
				case "daysMax":
					b.DaysMax = p
				default:
					if p == nil || *p < 1 {
						return bad("position must be a whole number from 1")
					}
					b.Position = *p
				}
			}
		default:
			return bad("Unknown field: %s", k)
		}
		if err != nil {
			return err
		}
	}
	if b.Name == "" {
		return bad("name is required")
	}
	if b.ArrMin != nil && b.ArrMax != nil && *b.ArrMin >= *b.ArrMax {
		return bad("arrMin must be less than arrMax")
	}
	if b.DaysMin != nil && b.DaysMax != nil && *b.DaysMin > *b.DaysMax {
		return bad("daysMin must not be greater than daysMax")
	}
	return nil
}

func bucketNameTaken(q querier, name string, exceptID int64) (bool, error) {
	var one int
	err := q.QueryRow("SELECT 1 FROM buckets WHERE name = ? COLLATE NOCASE AND id <> ?", name, exceptID).Scan(&one)
	if err == sql.ErrNoRows {
		return false, nil
	}
	return err == nil, err
}

// reorderRuleBuckets rewrites positions 1..n, placing the bucket `moveID` at `to`
// (moveID 0 just closes gaps).
func reorderRuleBuckets(q querier, moveID int64, to int) error {
	all, err := listBuckets(q)
	if err != nil {
		return err
	}
	var ids []int64
	for _, b := range all {
		if b.Kind == "rule" && b.ID != moveID {
			ids = append(ids, b.ID)
		}
	}
	if moveID != 0 {
		to = max(1, min(to, len(ids)+1))
		ids = append(ids[:to-1], append([]int64{moveID}, ids[to-1:]...)...)
	}
	for i, id := range ids {
		if _, err := q.Exec("UPDATE buckets SET position = ? WHERE id = ?", i+1, id); err != nil {
			return err
		}
	}
	return nil
}

func insertBucket(q querier, b bucket) (int64, error) {
	res, err := q.Exec(`INSERT INTO buckets(name, description, color, kind, position, autorenew, arr_min, arr_max, days_min, days_max)
		VALUES(?, ?, ?, 'rule', ?, ?, ?, ?, ?, ?)`,
		b.Name, b.Description, b.Color, b.Position, b.Autorenew, b.ArrMin, b.ArrMax, b.DaysMin, b.DaysMax)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// mutateBuckets runs a change in a transaction and refreshes the cache afterwards.
func (a *App) mutateBuckets(fn func(tx *sql.Tx) error) error {
	if err := inTx(a.db, fn); err != nil {
		return err
	}
	return loadBuckets(a.db)
}

func (a *App) createBucket(body any) (int64, error) {
	b := bucket{Kind: "rule", Autorenew: "any", Color: "#3c6fd6"}
	if err := applyBucketFields(&b, body); err != nil {
		return 0, err
	}
	var id int64
	err := a.mutateBuckets(func(tx *sql.Tx) error {
		if taken, err := bucketNameTaken(tx, b.Name, 0); err != nil {
			return err
		} else if taken {
			return &httpError{409, fmt.Sprintf("A bucket named %q already exists", b.Name)}
		}
		to := b.Position
		if to == 0 {
			to = math.MaxInt32 // not given: add at the end
		}
		var err error
		if id, err = insertBucket(tx, b); err != nil {
			return err
		}
		if err := reorderRuleBuckets(tx, id, to); err != nil {
			return err
		}
		return logActivity(tx, 0, "", "bucket", nil, "Added "+b.summaryLine())
	})
	return id, err
}

func (a *App) updateBucket(id int64, body any) error {
	old, ok := bucketByID(id)
	if !ok {
		return &httpError{404, fmt.Sprintf("Bucket %d not found", id)}
	}
	b := old
	if err := applyBucketFields(&b, body); err != nil {
		return err
	}
	return a.mutateBuckets(func(tx *sql.Tx) error {
		if taken, err := bucketNameTaken(tx, b.Name, id); err != nil {
			return err
		} else if taken {
			return &httpError{409, fmt.Sprintf("A bucket named %q already exists", b.Name)}
		}
		if _, err := tx.Exec(`UPDATE buckets SET name = ?, description = ?, color = ?, autorenew = ?,
			arr_min = ?, arr_max = ?, days_min = ?, days_max = ? WHERE id = ?`,
			b.Name, b.Description, b.Color, b.Autorenew, b.ArrMin, b.ArrMax, b.DaysMin, b.DaysMax, id); err != nil {
			return err
		}
		if b.Kind == "rule" && b.Position != old.Position {
			if err := reorderRuleBuckets(tx, id, b.Position); err != nil {
				return err
			}
			if err := logActivity(tx, 0, "", "bucket", nil, fmt.Sprintf("Moved %s to position %d", b.Name, b.Position)); err != nil {
				return err
			}
		}
		if old.summaryLine() != b.summaryLine() {
			return logActivity(tx, 0, "", "bucket", old.summaryLine(), b.summaryLine())
		}
		return nil
	})
}

func (a *App) deleteBucket(id int64) error {
	b, ok := bucketByID(id)
	if !ok {
		return &httpError{404, fmt.Sprintf("Bucket %d not found", id)}
	}
	if b.Kind != "rule" {
		return bad("%q is a built-in bucket and cannot be deleted", b.Name)
	}
	return a.mutateBuckets(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM buckets WHERE id = ?", id); err != nil {
			return err
		}
		if err := reorderRuleBuckets(tx, 0, 0); err != nil {
			return err
		}
		return logActivity(tx, 0, "", "bucket", "Deleted "+b.summaryLine(), nil)
	})
}

// addDefaultBuckets adds the standard set. It refuses when rule buckets already exist,
// so it can never clobber a customised scheme.
func (a *App) addDefaultBuckets() error {
	return a.mutateBuckets(func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow("SELECT COUNT(*) FROM buckets WHERE kind = 'rule'").Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return &httpError{409, "Buckets with conditions already exist; delete them first to load the standard set"}
		}
		for i, b := range defaultBuckets() {
			b.Position = i + 1
			if taken, err := bucketNameTaken(tx, b.Name, 0); err != nil {
				return err
			} else if taken {
				return &httpError{409, fmt.Sprintf("A bucket named %q already exists", b.Name)}
			}
			if _, err := insertBucket(tx, b); err != nil {
				return err
			}
		}
		return logActivity(tx, 0, "", "bucket", nil, "Added the standard six buckets")
	})
}
