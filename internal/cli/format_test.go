package cli

import (
	"strings"
	"testing"
	"time"
	// The zones the tests name, wherever they run.
	_ "time/tzdata"
)

func TestMoneyIsPrintedAsTheConsolePrintsIt(t *testing.T) {
	cases := []struct {
		micro    string
		decimals int
		want     string
	}{
		{"0", 2, "R$ 0,00"},
		{"1234560000", 2, "R$ 1.234,56"},
		{"1234565000", 2, "R$ 1.234,57"}, // half up
		{"-50000", 2, "-R$ 0,05"},
		{"62500", 4, "R$ 0,0625"},
		{"60000", 4, "R$ 0,06"}, // zeros past the cent dropped
		{"123456789000000", 2, "R$ 123.456.789,00"},
		{"x", 2, "—"},
	}
	for _, c := range cases {
		if got := money(c.micro, c.decimals); got != c.want {
			t.Errorf("money(%s, %d) = %s, want %s", c.micro, c.decimals, got, c.want)
		}
	}
}

func TestReaisAreReadAsCents(t *testing.T) {
	for in, want := range map[string]int64{"100": 10000, "49,90": 4990, "49.9": 4990, "R$ 10": 1000, "0,05": 5} {
		got, err := parseReais(in)
		if err != nil || got != want {
			t.Errorf("parseReais(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "0", "-5", "1.000,00", "abc", "10,999"} {
		if _, err := parseReais(bad); err == nil {
			t.Errorf("parseReais(%q) accepted", bad)
		}
	}
}

func TestFolderNamesBecomeServiceNames(t *testing.T) {
	for in, want := range map[string]string{
		"Minha Loja": "minha-loja", "api_v2": "api-v2", "2024-site": "app-2024-site",
		"--x--": "x", "Ação": "a-o", "": "app",
	} {
		if got := nameFrom(in); got != want {
			t.Errorf("nameFrom(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDotenvValuesAreQuotedAsTheAPIExportsThem(t *testing.T) {
	for in, want := range map[string]string{
		"plain":      "plain",
		"":           "",
		"two words":  `"two words"`,
		`say "hi"`:   `"say \"hi\""`,
		"a\nb":       `"a\nb"`,
		"$HOME":      `"\$HOME"`,
		`back\slash`: `"back\\slash"`,
		"it's":       `"it's"`,
		"#tag":       `"#tag"`,
		"tab\there":  `"tab\there"`,
	} {
		if got := dotenvValue(in); got != want {
			t.Errorf("dotenvValue(%q) = %s, want %s", in, got, want)
		}
	}
}

func TestADayEndsAtItsLastSecondWhereTheMachineIs(t *testing.T) {
	cases := []struct{ zone, day, want string }{
		// The end of the 31st in Brasília, not midnight UTC (21:00 the day before).
		{"America/Sao_Paulo", "2027-01-31", "2027-02-01T02:59:59Z"},
		// Days the clocks move are 23 and 25 hours long: never an hour late or early.
		{"America/Chicago", "2026-03-08", "2026-03-09T04:59:59Z"},
		{"America/Chicago", "2026-11-01", "2026-11-02T05:59:59Z"},
		{"UTC", " 2027-01-31 ", "2027-01-31T23:59:59Z"},
	}
	for _, c := range cases {
		loc, err := time.LoadLocation(c.zone)
		if err != nil {
			t.Fatal(err)
		}
		got, err := endOfDayIn(c.day, loc)
		if err != nil || got.UTC().Format(time.RFC3339) != c.want {
			t.Errorf("%q in %s = %v, %v; want %s", c.day, c.zone, got.UTC(), err, c.want)
		}
	}
	for _, bad := range []string{"2027-02-31", "31/01/2027", "2027-01-31T10:00", "tomorrow", ""} {
		if _, err := endOfDay(bad); err == nil {
			t.Errorf("endOfDay(%q) accepted", bad)
		}
	}
}

func TestAZonedMomentNamesItsZone(t *testing.T) {
	iso := "2027-02-01T02:59:59.000Z"
	parsed, _ := time.Parse(time.RFC3339Nano, iso)
	want := parsed.Local().Format("2006-01-02 15:04:05 MST")
	if got := whenZoned(iso); got != want || !strings.Contains(got, ":59:59 ") {
		t.Errorf("whenZoned = %q, want %q", got, want)
	}
	if got := whenZoned("not a time"); got != "not a time" {
		t.Errorf("whenZoned(junk) = %q", got)
	}
}
