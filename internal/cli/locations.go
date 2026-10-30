package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/masterdaweb/mw-cli/internal/api"
)

// Where projects and databases are made: the datacenters `mw regions` lists,
// each one's serverless apps and databases offered where it can take new ones
// now. A --location names one by its id, its name or its code, whatever the
// case or accents: 3, dallas, "Belo Horizonte", br-bh.

// location is one of GET /v1/serverless/locations or /v1/databases/locations.
type location struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	RegionName *string `json:"region_name"`
	// Deployable: it takes new ones now; false is temporarily unavailable.
	Deployable bool `json:"deployable"`
}

const (
	appLocations = "/serverless/locations"
	dbLocations  = "/databases/locations"
)

// locations reads one of the two lists, as an array or a list page.
func (a *App) locations(ctx context.Context, path string) ([]location, error) {
	c, err := a.Client()
	if err != nil {
		return nil, err
	}
	var raw json.RawMessage
	if err := c.Do(ctx, "GET", path, nil, nil, &raw); err != nil {
		return nil, err
	}
	var all []location
	if json.Unmarshal(raw, &all) == nil {
		return all, nil
	}
	var page api.Page[location]
	if err := json.Unmarshal(raw, &page); err != nil {
		return nil, fmt.Errorf("unexpected locations from %s: %w", path, err)
	}
	return page.Data, nil
}

// resolveLocation is the datacenter_id a --location names among the
// locations at path, refused with what to pick when it names none, or one
// that cannot take new ones now (what: "projects", "databases").
func (a *App) resolveLocation(ctx context.Context, path, what, ref string) (int64, error) {
	all, err := a.locations(ctx, path)
	if err != nil {
		return 0, err
	}
	key := locationKey(ref)
	var open []string
	for _, l := range all {
		if l.Deployable {
			open = append(open, l.Name)
		}
		if strconv.FormatInt(l.ID, 10) != strings.TrimSpace(ref) && locationKey(l.Name) != key &&
			(l.RegionName == nil || locationKey(*l.RegionName) != key) {
			continue
		}
		if !l.Deployable {
			return 0, fmt.Errorf("%s is temporarily unavailable for new %s: choose another (`mw regions`)", l.Name, what)
		}
		return l.ID, nil
	}
	if len(open) == 0 {
		return 0, fmt.Errorf("no location takes new %s right now", what)
	}
	return 0, fmt.Errorf("no location %q: one of %s (`mw regions`)", ref, strings.Join(open, ", "))
}

// locationKey compares names regardless of case, accents, spaces and dashes.
func locationKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case 'á', 'à', 'â', 'ã', 'ä':
			r = 'a'
		case 'é', 'è', 'ê', 'ë':
			r = 'e'
		case 'í', 'ì', 'î', 'ï':
			r = 'i'
		case 'ó', 'ò', 'ô', 'õ', 'ö':
			r = 'o'
		case 'ú', 'ù', 'û', 'ü':
			r = 'u'
		case 'ç':
			r = 'c'
		}
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// locationNames is each datacenter's name by id, for showing the ids given;
// not read when none is given, and empty when the regions cannot be read
// (the id is shown then).
func (a *App) locationNames(ctx context.Context, ids ...*int64) map[string]string {
	any := false
	for _, id := range ids {
		any = any || id != nil
	}
	if !any {
		return nil
	}
	c, err := a.Client()
	if err != nil {
		return nil
	}
	all, err := api.All[api.Region](ctx, c, "/regions", nil)
	if err != nil {
		return nil
	}
	out := make(map[string]string, len(all))
	for _, r := range all {
		out[r.ID] = r.Name
	}
	return out
}

// locationText is a datacenter's name, its id when the name is not known,
// or a dash when there is none.
func locationText(names map[string]string, id *int64) string {
	if id == nil {
		return "-"
	}
	key := strconv.FormatInt(*id, 10)
	if n, ok := names[key]; ok {
		return n
	}
	return key
}

// locationFlag is the --location every command that makes a project or a
// database takes.
const locationFlag = "where it runs, for good: a location's id, name or code (`mw regions`)"
