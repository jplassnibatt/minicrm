package main

// Pure business rules: bucket waterfall, fiscal quarters, inactive rule, account presentation.

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	cadenceOptions   = []string{"Weekly", "Biweekly", "Monthly", "Bi-Monthly", "Quarterly", "Bi-Annual"}
	renewalStatuses  = []string{"Red", "Yellow", "Purple", "Green"}
	contractOutcomes = []string{"Renewed", "Churned", "New CSM"}
)

const isoDate = "2006-01-02"

var (
	reISO = regexp.MustCompile(`^(\d{4})-(\d{1,2})-(\d{1,2})$`)
	reUS  = regexp.MustCompile(`^(\d{1,2})/(\d{1,2})/(\d{4})$`)
)

// parseDate normalises a date to ISO YYYY-MM-DD. It returns (nil, true) for "no date"
// (null, "", "—", "Month-to-Month") and ok=false when the value is not a real date.
func parseDate(v any) (iso *string, ok bool) {
	if v == nil {
		return nil, true
	}
	s, isStr := v.(string)
	if !isStr {
		return nil, false
	}
	s = strings.TrimSpace(s)
	if s == "" || s == "—" || strings.EqualFold(s, "month-to-month") {
		return nil, true
	}
	var y, m, d int
	if g := reISO.FindStringSubmatch(s); g != nil {
		y, _ = strconv.Atoi(g[1])
		m, _ = strconv.Atoi(g[2])
		d, _ = strconv.Atoi(g[3])
	} else if g := reUS.FindStringSubmatch(s); g != nil {
		m, _ = strconv.Atoi(g[1])
		d, _ = strconv.Atoi(g[2])
		y, _ = strconv.Atoi(g[3])
	} else {
		return nil, false
	}
	t := time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC)
	if t.Year() != y || int(t.Month()) != m || t.Day() != d { // e.g. Feb 31 rolls over
		return nil, false
	}
	out := t.Format(isoDate)
	return &out, true
}

func todayISO() string { return time.Now().Format(isoDate) }

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

// daysBetween returns whole days from one ISO date to another (positive when `to` is later).
func daysBetween(from, to string) int {
	a, _ := time.Parse(isoDate, from[:10])
	b, _ := time.Parse(isoDate, to[:10])
	return int(math.Round(b.Sub(a).Hours() / 24))
}

// fiscalQuarter: the fiscal year starts in February (Feb–Apr = Q1 … Nov–Jan = Q4);
// calendar 2026 = FY2027. Returns the label and a sortable ordinal.
func fiscalQuarter(termEnd *string) (label string, ord int, ok bool) {
	if termEnd == nil {
		return "", 0, false
	}
	year, _ := strconv.Atoi((*termEnd)[:4])
	month, _ := strconv.Atoi((*termEnd)[5:7])
	q := 4
	switch {
	case month >= 2 && month <= 4:
		q = 1
	case month >= 5 && month <= 7:
		q = 2
	case month >= 8 && month <= 10:
		q = 3
	}
	fy := year + 1
	if month == 1 {
		fy = year
	}
	return fmt.Sprintf("FY%d Q%d", fy, q), fy*10 + q, true
}

// row is one record of the accounts table.
type row struct {
	ID                     int64
	Name                   string
	Arr                    float64
	TermEnd                *string
	Autorenew              *bool
	AccountOwner           string
	AccountLink            string // Salesforce URL
	AccountOwnerLink       string // Slack URL
	SlackChannel           string // free text; the UI renders [name](https://…) as a link
	RenewalManagerLink     string // Slack URL
	Details                string // one free-text box, overwritten on edit (unlike Notes)
	RenewalStatus          string
	Ews                    string
	RenewalManager         string
	LastEmail              *string
	LastGong               *string
	SourceActive           bool
	DroppedAt              *string
	Risk                   *string
	Cadence                *string
	Notes                  []noteItem // newest first
	RenewalManagerOverride *string
	ContractOutcome        *string
	ContractNewArr         *float64
	ContractNewTermEnd     *string
	ContractNewAutorenew   *bool
	ContractUpdatedAt      *string
	CreatedAt              string
	UpdatedAt              string
	ClaudeUpdatedAt        *string
	ManualUpdatedAt        *string
}

type contractInfo struct {
	Outcome      *string  `json:"outcome"`
	NewArr       *float64 `json:"newArr"`
	NewTermEnd   *string  `json:"newTermEnd"`
	NewAutorenew *bool    `json:"newAutorenew"`
	UpdatedAt    *string  `json:"updatedAt"`
}

type sourceInfo struct {
	Arr            float64 `json:"arr"`
	TermEnd        *string `json:"termEnd"`
	Autorenew      *bool   `json:"autorenew"`
	RenewalStatus  string  `json:"renewalStatus"`
	RenewalManager string  `json:"renewalManager"`
}

// Account is the API representation of an account.
type Account struct {
	ID                 int64        `json:"id"`
	Name               string       `json:"name"`
	Arr                float64      `json:"arr"`
	TermEnd            *string      `json:"termEnd"`
	Days               *int         `json:"days"`
	Autorenew          *bool        `json:"autorenew"`
	BucketID           int64        `json:"bucketId"`
	BucketName         string       `json:"bucketName"`
	TermType           string       `json:"termType"`
	FiscalQuarter      *string      `json:"fiscalQuarter"`
	AccountOwner       string       `json:"accountOwner"`
	AccountLink        string       `json:"accountLink"`
	AccountOwnerLink   string       `json:"accountOwnerLink"`
	SlackChannel       string       `json:"slackChannel"`
	RenewalStatus      string       `json:"renewalStatus"`
	Ews                *float64     `json:"ews"` // null when not set
	RenewalManager     string       `json:"renewalManager"`
	RenewalManagerLink string       `json:"renewalManagerLink"`
	LastEmail          *string      `json:"lastEmail"`
	LastEmailDaysAgo   *int         `json:"lastEmailDaysAgo"`
	LastGong           *string      `json:"lastGong"`
	LastGongDaysAgo    *int         `json:"lastGongDaysAgo"`
	Status             string       `json:"status"`
	InactiveReason     *string      `json:"inactiveReason"`
	InactiveSince      *string      `json:"inactiveSince"`
	Risk               *string      `json:"risk"`
	Cadence            *string      `json:"cadence"`
	Details            string       `json:"details"`
	Notes              []noteItem   `json:"notes"` // every note, newest first
	NotesUpdatedAt     *string      `json:"notesUpdatedAt"`
	NoteCount          int          `json:"noteCount"`
	Contract           contractInfo `json:"contract"`
	Source             sourceInfo   `json:"source"`
	CreatedAt          string       `json:"createdAt"`
	UpdatedAt          string       `json:"updatedAt"`
	ClaudeUpdatedAt    *string      `json:"claudeUpdatedAt,omitempty"`
	ManualUpdatedAt    *string      `json:"manualUpdatedAt,omitempty"`
}

func strPtr(s string) *string { return &s }

// present turns a DB row into the API representation, applying contract overrides and the
// waterfall as of the given date.
func present(r row, asOf string) Account {
	outcome := ""
	if r.ContractOutcome != nil {
		outcome = *r.ContractOutcome
	}
	arr, termEnd, autorenew, renewalStatus := r.Arr, r.TermEnd, r.Autorenew, r.RenewalStatus
	if outcome == "Renewed" {
		if r.ContractNewArr != nil {
			arr = *r.ContractNewArr
		}
		if r.ContractNewTermEnd != nil && *r.ContractNewTermEnd != "" {
			termEnd = r.ContractNewTermEnd
		}
		if r.ContractNewAutorenew != nil {
			autorenew = r.ContractNewAutorenew
		}
		renewalStatus = "Green"
	}

	var days *int
	if termEnd != nil {
		d := daysBetween(asOf, *termEnd)
		days = &d
	}
	bucket := bucketFor(arr, days, autorenew, termEnd)

	status := "Active"
	var inactiveReason, inactiveSince *string
	switch {
	case !r.SourceActive:
		status = "Inactive"
		inactiveReason = strPtr("Dropped from latest import (reassigned, or account no longer on file)")
		inactiveSince = r.DroppedAt
	case outcome == "Churned" || outcome == "New CSM":
		status = "Inactive"
		if outcome == "New CSM" {
			inactiveReason = strPtr("Reassigned to a new CSM")
		} else {
			inactiveReason = strPtr("Manually marked churned by CSM")
		}
		inactiveSince = r.ContractUpdatedAt
	case outcome != "Renewed" && arr == 0 && (days == nil || *days > 30):
		status = "Inactive"
		inactiveReason = strPtr("Churned — $0 ARR, no renewal due within 30 days")
	}

	a := Account{
		ID: r.ID, Name: r.Name, Arr: arr, TermEnd: termEnd, Days: days, Autorenew: autorenew,
		BucketID: bucket.ID, BucketName: bucket.Name, TermType: "Termed",
		AccountLink: r.AccountLink, AccountOwnerLink: r.AccountOwnerLink, SlackChannel: r.SlackChannel, RenewalManagerLink: r.RenewalManagerLink,
		AccountOwner: r.AccountOwner, RenewalStatus: renewalStatus, RenewalManager: r.RenewalManager,
		LastEmail: r.LastEmail, LastGong: r.LastGong,
		Status: status, InactiveReason: inactiveReason, InactiveSince: inactiveSince,
		Risk: r.Risk, Cadence: r.Cadence, Details: r.Details, Notes: r.Notes, NoteCount: len(r.Notes),
		Contract:  contractInfo{r.ContractOutcome, r.ContractNewArr, r.ContractNewTermEnd, r.ContractNewAutorenew, r.ContractUpdatedAt},
		Source:    sourceInfo{r.Arr, r.TermEnd, r.Autorenew, r.RenewalStatus, r.RenewalManager},
		CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
		ClaudeUpdatedAt: r.ClaudeUpdatedAt, ManualUpdatedAt: r.ManualUpdatedAt,
	}
	if len(a.Notes) == 0 {
		a.Notes = []noteItem{} // JSON [] rather than null
	} else {
		a.NotesUpdatedAt = &a.Notes[0].CreatedAt
	}
	if n, err := strconv.ParseFloat(r.Ews, 64); err == nil {
		a.Ews = &n
	}
	if termEnd == nil {
		a.TermType = "Evergreen"
	}
	if label, _, ok := fiscalQuarter(termEnd); ok {
		a.FiscalQuarter = &label
	}
	if r.RenewalManagerOverride != nil && *r.RenewalManagerOverride != "" {
		a.RenewalManager = *r.RenewalManagerOverride
	}
	if r.LastEmail != nil {
		d := daysBetween(*r.LastEmail, asOf)
		a.LastEmailDaysAgo = &d
	}
	if r.LastGong != nil {
		d := daysBetween(*r.LastGong, asOf)
		a.LastGongDaysAgo = &d
	}
	return a
}
