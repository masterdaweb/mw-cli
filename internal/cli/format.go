package cli

import (
	"math/big"
	"strconv"
	"strings"
	"time"
)

// money prints µ¢ (the API's unit: R$ 1 = 1 000 000) as reais, the way the
// console does (R$ 1.234,56), rounded half up. decimals > 2 keeps more of a
// small price (R$ 0,0625), trailing zeros past the cent dropped.
func money(micro string, decimals int) string {
	v, ok := new(big.Int).SetString(strings.TrimSpace(micro), 10)
	if !ok {
		return "—"
	}
	if decimals < 2 {
		decimals = 2
	}
	if decimals > 6 {
		decimals = 6
	}
	neg := v.Sign() < 0
	v.Abs(v)
	// µ¢ → units of 10^-decimals reais, rounded half up.
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(6-decimals)), nil)
	v.Add(v, new(big.Int).Div(scale, big.NewInt(2)))
	v.Div(v, scale)
	digits := v.String()
	for len(digits) <= decimals {
		digits = "0" + digits
	}
	whole, frac := digits[:len(digits)-decimals], digits[len(digits)-decimals:]
	if decimals > 2 {
		frac = strings.TrimRight(frac, "0")
		for len(frac) < 2 {
			frac += "0"
		}
	}
	var grouped []string
	for len(whole) > 3 {
		grouped = append([]string{whole[len(whole)-3:]}, grouped...)
		whole = whole[:len(whole)-3]
	}
	grouped = append([]string{whole}, grouped...)
	out := "R$ " + strings.Join(grouped, ".") + "," + frac
	if neg {
		out = "-" + out
	}
	return out
}

func moneyPtr(micro *string, decimals int) string {
	if micro == nil {
		return "—"
	}
	return money(*micro, decimals)
}

// bytesText prints a decimal byte count (as the bills count them: 1 GB = 10^9).
func bytesText(raw string) string {
	n, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return raw
	}
	units := []string{"B", "KB", "MB", "GB", "TB", "PB"}
	i := 0
	for n >= 1000 && i < len(units)-1 {
		n /= 1000
		i++
	}
	if i == 0 {
		return strconv.FormatFloat(n, 'f', 0, 64) + " B"
	}
	return strconv.FormatFloat(n, 'f', 1, 64) + " " + units[i]
}

// when prints an API timestamp in the local time zone, to the minute.
func when(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02 15:04")
}

// dueDay prints the day an invoice is due. The API sends a due day as noon UTC
// of it, so the UTC date is the day whatever the local zone.
func dueDay(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return iso
	}
	return t.UTC().Format("2006-01-02")
}

// endOfDay reads a day (2027-01-31) as its last second where this machine
// is: 23:59:59 in its time zone. TZ names another, and the binary carries
// the zone database (cmd/mw imports time/tzdata), so TZ works in a
// container too, where the zone is otherwise UTC. Never midnight plus
// 24 hours: a day the clocks move is 23 or 25 hours long.
func endOfDay(s string) (time.Time, error) {
	return endOfDayIn(s, time.Local)
}

func endOfDayIn(s string, loc *time.Location) (time.Time, error) {
	d, err := time.ParseInLocation("2006-01-02", strings.TrimSpace(s), loc)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(d.Year(), d.Month(), d.Day(), 23, 59, 59, 0, loc), nil
}

// whenZoned prints a moment here with its zone, for when where the day ends
// matters: "2027-01-31 23:59:59 -03", "… UTC" in a container.
func whenZoned(iso string) string {
	t, err := time.Parse(time.RFC3339Nano, iso)
	if err != nil {
		return iso
	}
	return t.Local().Format("2006-01-02 15:04:05 MST")
}

func whenPtr(iso *string) string {
	if iso == nil || *iso == "" {
		return "—"
	}
	return when(*iso)
}

// onOff is a switch as a sentence says it.
func onOff(on bool) string {
	if on {
		return "on"
	}
	return "off"
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
