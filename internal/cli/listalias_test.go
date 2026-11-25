package cli

import (
	"fmt"
	"io"
	"strings"
	"testing"
	"text/tabwriter"
)

// `mw db list` was "unknown command" (production check, 2026-11-25).
func TestAListingGroupAnswersToListAndLsAsItsBareForm(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	f.answer("GET /v1/databases", 200, []interface{}{database("running")})
	bare, _, err := run(t, srv, t.TempDir(), "db")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bare, "PostgreSQL 18") {
		t.Fatalf("bare listing %q", bare)
	}
	for _, verb := range []string{"list", "ls"} {
		out, _, err := run(t, srv, t.TempDir(), "db", verb)
		if err != nil || out != bare {
			t.Fatalf("mw db %s: %q %v, want %q", verb, out, err, bare)
		}
	}
	// The flags of every format come along: -q prints the names alone.
	out, _, err := run(t, srv, t.TempDir(), "db", "list", "-q")
	if err != nil || out != "loja-prod\n" {
		t.Fatalf("mw db list -q: %q %v", out, err)
	}
}

// `mw db -q` printed the whole table: -q and --no-headers act on every list,
// not only the ones printed through printList.
func TestQuietAndNoHeadersActOnEveryList(t *testing.T) {
	f, srv := newFakeAPI(t)
	withLocations(f)
	other := database("running")
	other["name"] = "loja-staging"
	f.answer("GET /v1/databases", 200, []interface{}{database("running"), other})
	out, _, err := run(t, srv, t.TempDir(), "db", "-q")
	if err != nil || out != "loja-prod\nloja-staging\n" {
		t.Fatalf("mw db -q: %q %v", out, err)
	}
	full, _, _ := run(t, srv, t.TempDir(), "db")
	out, _, err = run(t, srv, t.TempDir(), "db", "--no-headers")
	if err != nil || strings.Contains(out, "ENGINE") || !strings.HasSuffix(full, out) || strings.Count(out, "\n") != 2 {
		t.Fatalf("mw db --no-headers: %q %v (full %q)", out, err, full)
	}
	// An empty list prints nothing for a pipe to read as an id.
	f.answer("GET /v1/databases", 200, []interface{}{})
	out, _, err = run(t, srv, t.TempDir(), "db", "-q")
	if err != nil || out != "" {
		t.Fatalf("mw db -q, no databases: %q %v", out, err)
	}
}

// What is not a list prints as it was: -q never cuts a detail or a sentence.
func TestQuietLeavesWhatIsNotAListAlone(t *testing.T) {
	var out strings.Builder
	a := &App{Out: &out, Quiet: true}
	if err := a.print(nil, func(w *tabwriter.Writer) {
		fmt.Fprintln(w, "Credit\tR$ 149,27")
		fmt.Fprintln(w, "Lasts\tabout 25 days")
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "Credit") || !strings.Contains(out.String(), "about 25 days") {
		t.Fatalf("detail under -q: %q", out.String())
	}
}

func TestAListAliasReadsTheGroupsOwnFlags(t *testing.T) {
	root := NewRoot(&App{Version: "test", In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	group, _, err := root.Find([]string{"groups"})
	if err != nil {
		t.Fatal(err)
	}
	list, _, err := root.Find([]string{"groups", "list"})
	if err != nil || list.Name() != "list" {
		t.Fatalf("no mw groups list: %v", err)
	}
	if list.Flags().Lookup("project") != group.Flags().Lookup("project") {
		t.Fatal("mw groups list --project is not the flag mw groups reads")
	}
	// Without it, the same answer as the bare form.
	_, srv := newFakeAPI(t)
	_, _, bareErr := run(t, srv, t.TempDir(), "groups")
	_, _, listErr := run(t, srv, t.TempDir(), "groups", "list")
	if bareErr == nil || listErr == nil || bareErr.Error() != listErr.Error() {
		t.Fatalf("mw groups: %v; mw groups list: %v", bareErr, listErr)
	}
}

func TestListAliasesOnlyWhereTheBareFormLists(t *testing.T) {
	root := NewRoot(&App{Version: "test", In: strings.NewReader(""), Out: io.Discard, Err: io.Discard})
	for _, name := range listingGroups {
		c, _, err := root.Find([]string{name, "list"})
		if err != nil || c.Name() != "list" || c.Parent().Name() != name {
			t.Fatalf("mw %s list: %v", name, err)
		}
	}
	for _, name := range []string{"deploy", "security", "wallet"} {
		if c, _, err := root.Find([]string{name, "list"}); err == nil && c.Name() == "list" {
			t.Fatalf("mw %s list must not exist: the bare form does not list", name)
		}
	}
	// `mw storage ls` still lists a bucket's objects.
	ls, _, err := root.Find([]string{"storage", "ls"})
	if err != nil || ls.Name() != "ls" || ls.Hidden {
		t.Fatalf("mw storage ls changed: %v %+v", err, ls)
	}
	// The existing `mw vpn list` stays as it was.
	vpn, _, err := root.Find([]string{"vpn", "list"})
	if err != nil || vpn.Hidden {
		t.Fatalf("mw vpn list changed: %v", err)
	}
}
