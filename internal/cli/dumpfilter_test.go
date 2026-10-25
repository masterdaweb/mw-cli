package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestAnArchiveIsRestoredWithoutItsExtensionStatements(t *testing.T) {
	list := `;
; Archive created at 2026-10-24 20:00:00 UTC
;     dbname: loja_prod
;
2; 3079 16386 EXTENSION - vector 
4107; 0 0 COMMENT - EXTENSION vector 
3; 3079 16400 EXTENSION - postgis 
4108; 0 0 COMMENT - EXTENSION plpgsql 
215; 1259 16729 TABLE public pedidos loja_prod
4101; 0 16729 TABLE DATA public pedidos loja_prod
4109; 0 0 COMMENT public TABLE pedidos loja_prod
`
	kept, exts := filterArchiveList(list)
	if strings.Contains(kept, "EXTENSION") {
		t.Fatalf("an extension entry is kept:\n%s", kept)
	}
	for _, want := range []string{"TABLE public pedidos", "TABLE DATA public pedidos", "COMMENT public TABLE pedidos", "; Archive created"} {
		if !strings.Contains(kept, want) {
			t.Fatalf("%q was dropped:\n%s", want, kept)
		}
	}
	if strings.Join(exts, ",") != "postgis,vector" {
		t.Fatalf("extensions %v", exts)
	}
	if m := missingExtensions(exts, []string{"vector", "pg_trgm"}); strings.Join(m, ",") != "postgis" {
		t.Fatalf("missing %v", m)
	}
}

func TestAPlainDumpIsReadWithoutItsExtensionStatements(t *testing.T) {
	dump := `SET statement_timeout = 0;
CREATE EXTENSION IF NOT EXISTS vector WITH SCHEMA public;
COMMENT ON EXTENSION vector IS 'vector data type and ivfflat and hnsw access methods';
CREATE EXTENSION IF NOT EXISTS "uuid-ossp" WITH SCHEMA public;
COMMENT ON EXTENSION "uuid-ossp" IS 'generate universally unique identifiers
over two lines';
COMMENT ON EXTENSION plpgsql IS 'PL/pgSQL procedural language';
CREATE TABLE public.pedidos (id integer NOT NULL, item text);
COMMENT ON TABLE public.pedidos IS 'Os pedidos da loja';
COPY public.pedidos (id, item) FROM stdin;
1	pão de queijo
\.
`
	var out bytes.Buffer
	exts, err := filterSQL(strings.NewReader(dump), &out)
	if err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if strings.Contains(got, "EXTENSION") || strings.Contains(got, "over two lines") {
		t.Fatalf("an extension statement is kept:\n%s", got)
	}
	for _, want := range []string{"SET statement_timeout", "CREATE TABLE public.pedidos", "COMMENT ON TABLE public.pedidos", "1\tpão de queijo", "\\."} {
		if !strings.Contains(got, want) {
			t.Fatalf("%q was dropped:\n%s", want, got)
		}
	}
	if strings.Join(exts, ",") != "uuid-ossp,vector" {
		t.Fatalf("extensions %v", exts)
	}
}
