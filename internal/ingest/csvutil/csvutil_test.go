package csvutil

import "testing"

func TestParseNumber(t *testing.T) {
	cases := map[string]float64{
		"$1,234.56": 1234.56, "+$6,034.83": 6034.83, "(12.34)": -12.34, "($996.20)": -996.20,
		"-5": -5, "6.28%": 6.28, "2,189.071568": 2189.071568, "": 0, "--": 0, "-$3.10": -3.10,
	}
	for in, want := range cases {
		got, err := ParseNumber(in)
		if err != nil || got != want {
			t.Errorf("ParseNumber(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseNumber("abc"); err == nil {
		t.Error("expected error")
	}
}

func TestParseDateRange(t *testing.T) {
	s, e, err := ParseDateRange("Jan 01, 2026 - Nov 25, 2026")
	if err != nil || s != "2026-01-01" || e != "2026-11-25" {
		t.Fatalf("got %s %s %v", s, e, err)
	}
	if _, _, err := ParseDateRange("Nov 25, 2026 - Jan 01, 2026"); err == nil {
		t.Fatal("expected error for reversed range")
	}
	if _, _, err := ParseDateRange("yesterday"); err == nil {
		t.Fatal("expected error")
	}
}
