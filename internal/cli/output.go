package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"gopkg.in/yaml.v3"
)

// Output formats: a table for people, JSON or YAML for scripts. `--json` is
// kept as the short form of `-o json`, so every script written for it still
// works.
const (
	outputTable = "table"
	outputJSON  = "json"
	outputYAML  = "yaml"
)

// setOutput settles the output flags before a command runs: -o wins, --json
// is -o json, and a machine format makes every prompt and progress line go to
// standard error so standard output is the answer alone.
func (a *App) setOutput() error {
	o := strings.ToLower(strings.TrimSpace(a.Output))
	switch o {
	case "", outputTable, "text":
		o = outputTable
	case outputJSON, outputYAML:
	case "yml":
		o = outputYAML
	default:
		return fmt.Errorf("-o is table, json or yaml (not %q)", a.Output)
	}
	if a.JSON && o == outputTable {
		o = outputJSON
	}
	a.Output = o
	a.JSON = o != outputTable
	return nil
}

// encode writes v as the machine format in use.
func (a *App) encode(v interface{}) error {
	if a.Output == outputYAML {
		return writeYAML(a.Out, v)
	}
	enc := json.NewEncoder(a.Out)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// writeYAML prints v as YAML, its fields in the order the API sent them (the
// JSON tags' order), not sorted: a person reads `id` and `name` first.
func writeYAML(w io.Writer, v interface{}) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	node, err := jsonToYAMLNode(json.NewDecoder(bytes.NewReader(raw)))
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(node); err != nil {
		return err
	}
	return enc.Close()
}

// jsonToYAMLNode reads one JSON value, keeping object keys in order.
func jsonToYAMLNode(dec *json.Decoder) (*yaml.Node, error) {
	dec.UseNumber()
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	return tokenNode(dec, tok)
}

func tokenNode(dec *json.Decoder, tok json.Token) (*yaml.Node, error) {
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				key, ok := kt.(string)
				if !ok {
					return nil, errors.New("object key is not a string")
				}
				vt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := tokenNode(dec, vt)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, val)
			}
			_, err := dec.Token() // }
			return n, err
		case '[':
			n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
			for dec.More() {
				vt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				val, err := tokenNode(dec, vt)
				if err != nil {
					return nil, err
				}
				n.Content = append(n.Content, val)
			}
			_, err := dec.Token() // ]
			return n, err
		}
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}, nil
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(t.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}, nil
	case bool:
		v := "false"
		if t {
			v = "true"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: v}, nil
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}, nil
	}
	return nil, fmt.Errorf("unexpected JSON token %v", tok)
}

// listing is a list as every new command prints it: the API's items for -o
// json|yaml, their ids alone for -q, else a table (headers unless
// --no-headers), or a sentence when there is nothing to list.
type listing struct {
	headers []string
	rows    [][]string
	ids     []string
	empty   string
}

func (l *listing) add(id string, cells ...string) {
	l.ids = append(l.ids, id)
	l.rows = append(l.rows, cells)
}

func (a *App) printList(v interface{}, l listing) error {
	if a.JSON {
		return a.encode(v)
	}
	if a.Quiet {
		for _, id := range l.ids {
			fmt.Fprintln(a.Out, id)
		}
		return nil
	}
	if len(l.rows) == 0 {
		if l.empty != "" {
			fmt.Fprintln(a.Out, l.empty)
		}
		return nil
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	if !a.NoHeaders {
		fmt.Fprintln(w, strings.Join(l.headers, "\t"))
	}
	for _, r := range l.rows {
		fmt.Fprintln(w, strings.Join(r, "\t"))
	}
	return w.Flush()
}

// printDetail prints one object: the machine format, or "Key  value" lines.
type detail struct {
	pairs [][2]string
	after []string
}

func (d *detail) add(key, value string) { d.pairs = append(d.pairs, [2]string{key, value}) }

func (a *App) printDetail(v interface{}, d detail) error {
	if a.JSON {
		return a.encode(v)
	}
	if a.Quiet {
		if len(d.pairs) > 0 {
			fmt.Fprintln(a.Out, d.pairs[0][1])
		}
		return nil
	}
	w := tabwriter.NewWriter(a.Out, 0, 4, 2, ' ', 0)
	for _, p := range d.pairs {
		fmt.Fprintf(w, "%s\t%s\n", p[0], p[1])
	}
	if err := w.Flush(); err != nil {
		return err
	}
	for _, line := range d.after {
		fmt.Fprintln(a.Out, line)
	}
	return nil
}

// orDash is a value, or a dash for nothing.
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

func orDashPtr(s *string) string {
	if s == nil {
		return "—"
	}
	return orDash(*s)
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}
