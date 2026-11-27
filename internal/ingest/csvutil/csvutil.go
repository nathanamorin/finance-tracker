// Package csvutil holds parsing helpers shared by CSV-based sources:
// lenient number/date parsing and header-name column lookup.
package csvutil

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nathanamorin/finance-tracker/internal/ingest"
)

// Header maps lower-cased, trimmed column names to their index. The first
// occurrence of a duplicated name wins.
type Header map[string]int

// NewHeader indexes a header row.
func NewHeader(rec []string) Header {
	h := make(Header, len(rec))
	for i, name := range rec {
		name = strings.ToLower(strings.TrimSpace(name))
		if name != "" {
			if _, dup := h[name]; !dup {
				h[name] = i
			}
		}
	}
	return h
}

// Has reports whether every given column is present.
func (h Header) Has(names ...string) bool {
	for _, n := range names {
		if _, ok := h[n]; !ok {
			return false
		}
	}
	return true
}

// Get returns the trimmed value of the first named column present in rec,
// or "" if none is.
func (h Header) Get(rec []string, names ...string) string {
	for _, n := range names {
		if i, ok := h[n]; ok && i < len(rec) {
			if v := strings.TrimSpace(rec[i]); v != "" {
				return v
			}
		}
	}
	return ""
}

// IsBlank reports whether every field is empty.
func IsBlank(rec []string) bool {
	for _, f := range rec {
		if strings.TrimSpace(f) != "" {
			return false
		}
	}
	return true
}

// ParseNumber parses money/quantity/percent strings leniently: "$1,234.56",
// "+$6,034.83", "(12.34)" (negative), "-5", "6.28%" (→ 6.28), "--" and ""
// (→ 0).
func ParseNumber(s string) (float64, error) {
	s = strings.TrimSpace(s)
	if s == "" || s == "--" || s == "-" {
		return 0, nil
	}
	neg := false
	if strings.HasPrefix(s, "(") && strings.HasSuffix(s, ")") {
		neg = true
		s = s[1 : len(s)-1]
	}
	s = strings.NewReplacer("$", "", ",", "", " ", "", "%", "").Replace(s)
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if neg {
		v = -v
	}
	return v, nil
}

var dateLayouts = []string{
	"2006-01-02", "01/02/2006", "1/2/2006", "01/02/06", "1/2/06", "01-02-2006",
	"Jan 02, 2006", "Jan 2, 2006", "January 2, 2006", "02-Jan-2006",
}

// ParseDate accepts ISO, US numeric and "Jan 02, 2006" style dates.
func ParseDate(s string) (ingest.Date, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty date")
	}
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return ingest.NewDate(t), nil
		}
	}
	return "", fmt.Errorf("unrecognized date %q", s)
}

// ParseDateRange parses "Jan 01, 2026 - Nov 25, 2026" (also "to" or an en
// dash as the separator).
func ParseDateRange(s string) (start, end ingest.Date, err error) {
	for _, sep := range []string{" - ", " – ", " to ", " — "} {
		if a, b, ok := strings.Cut(s, sep); ok {
			if start, err = ParseDate(a); err != nil {
				return "", "", err
			}
			if end, err = ParseDate(b); err != nil {
				return "", "", err
			}
			if end < start {
				return "", "", fmt.Errorf("date range %q ends before it starts", s)
			}
			return start, end, nil
		}
	}
	return "", "", fmt.Errorf("unrecognized date range %q", s)
}
