package cli

import "testing"

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
