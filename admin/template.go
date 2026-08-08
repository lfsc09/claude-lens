package admin

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

// pageTemplates lists every page file that defines "title"/"content"/
// (optionally) "scripts" and ends with {{ template "base" . }}.
var pageTemplates = []string{
	"dashboard.html",
	"exchanges.html",
	"exchange_detail.html",
	"prices.html",
	"404.html",
}

// ParseTemplates loads templates/base.html plus every page in
// pageTemplates, returning one *template.Template per page keyed by
// filename.
//
// Every page defines blocks under the same three names ("title", "content",
// "scripts") — parsing them all into one shared *template.Template (as a
// naive port of a Jinja {% extends %}/{% block %} setup might do) would let
// whichever page parses last silently overwrite every other page's blocks,
// since Go's html/template has no per-page namespacing for defined
// templates within one Template value. Cloning the parsed base for each
// page keeps their blocks isolated.
func ParseTemplates() (map[string]*template.Template, error) {
	base, err := template.New("").Funcs(FuncMap()).ParseFS(templateFS, "templates/base.html")
	if err != nil {
		return nil, fmt.Errorf("parse base.html: %w", err)
	}

	pages := make(map[string]*template.Template, len(pageTemplates))
	for _, name := range pageTemplates {
		clone, err := base.Clone()
		if err != nil {
			return nil, fmt.Errorf("clone base for %s: %w", name, err)
		}
		clone, err = clone.ParseFS(templateFS, "templates/"+name)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		pages[name] = clone
	}
	return pages, nil
}

// routes is the admin UI's named-route table, consumed by urlFor. Keep it
// next to server.go's route registration so the two can't drift silently.
var routes = map[string]string{
	"ui_dashboard": "/",
	"ui_reset":     "/ui/reset",
	"ui_prices":    "/ui/prices",
	"ui_exchanges": "/ui/exchanges",
}

// urlFor resolves a named route to a path. For "ui_exchange_detail" pass the
// exchange id as the single positional arg; for routes that take query
// parameters (currently only "ui_exchanges"), pass alternating string
// key/value pairs — a zero-value ("" or 0) value omits that parameter,
// matching the old Jinja templates' `{% if session_id %}` guards.
func urlFor(name string, args ...any) (string, error) {
	if name == "ui_exchange_detail" {
		if len(args) != 1 {
			return "", fmt.Errorf("urlFor %q: expected exactly one arg (id)", name)
		}
		return fmt.Sprintf("/ui/exchanges/%v", args[0]), nil
	}

	base, ok := routes[name]
	if !ok {
		return "", fmt.Errorf("urlFor: unknown route %q", name)
	}
	if len(args) == 0 {
		return base, nil
	}
	if len(args)%2 != 0 {
		return "", fmt.Errorf("urlFor %q: odd number of key/value args", name)
	}

	values := url.Values{}
	for i := 0; i < len(args); i += 2 {
		key, ok := args[i].(string)
		if !ok {
			return "", fmt.Errorf("urlFor %q: key must be a string", name)
		}
		val := fmt.Sprint(args[i+1])
		if val == "" {
			continue
		}
		values.Set(key, val)
	}
	if len(values) == 0 {
		return base, nil
	}
	return base + "?" + values.Encode(), nil
}

// fmtCost mirrors the old Python _fmt_cost: nil -> "—", tiny costs get 4
// decimal places so they don't round to $0.00, everything else gets 2.
func fmtCost(c *float64) string {
	if c == nil {
		return "—"
	}
	if *c < 0.01 {
		return fmt.Sprintf("$%.4f", *c)
	}
	return fmt.Sprintf("$%.2f", *c)
}

// fmtInt comma-groups an integer, mirroring Python's "{:,}".format(n).
// Accepts int, int64, or a pointer to either; nil renders as "—".
func fmtInt(v any) string {
	n, ok := toInt64(v)
	if !ok {
		return "—"
	}
	return groupThousands(n)
}

func toInt64(v any) (int64, bool) {
	switch t := v.(type) {
	case int:
		return int64(t), true
	case int64:
		return t, true
	case *int:
		if t == nil {
			return 0, false
		}
		return int64(*t), true
	case *int64:
		if t == nil {
			return 0, false
		}
		return *t, true
	default:
		return 0, false
	}
}

func groupThousands(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	if neg {
		return "-" + string(out)
	}
	return string(out)
}

// fmtTime mirrors Python's _fmt_ts: a Unix timestamp formatted in local
// time as "YYYY-MM-DD HH:MM:SS".
func fmtTime(ts float64) string {
	if ts == 0 {
		return "—"
	}
	return time.Unix(int64(ts), 0).Format("2006-01-02 15:04:05")
}

// coalesce returns the first argument that isn't a nil pointer, an empty
// string, or a zero value — e.g. {{ coalesce .SessionName .SessionID }}.
func coalesce(vals ...any) any {
	for _, v := range vals {
		if isZero(v) {
			continue
		}
		return v
	}
	if len(vals) == 0 {
		return nil
	}
	return vals[len(vals)-1]
}

func isZero(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			return true
		}
		rv = rv.Elem()
	}
	return rv.IsZero()
}

// toJSON marshals v for embedding inside an inline <script> block. It
// escapes '<', '>', and '&' to \uXXXX sequences (the same defense Flask's
// |tojson filter applies) so a value containing "</script>" can't break out
// of the surrounding tag.
func toJSON(v any) (template.JS, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	s := string(b)
	s = strings.ReplaceAll(s, "<", "\\u003c")
	s = strings.ReplaceAll(s, ">", "\\u003e")
	s = strings.ReplaceAll(s, "&", "\\u0026")
	return template.JS(s), nil
}

// prettyJSON re-indents a JSON string for display; a value that isn't valid
// JSON (or nil) is returned unchanged, matching the old Python version's
// try/except fallback to the raw stored text.
//
// This reformats the raw bytes with json.Indent rather than round-tripping
// through Unmarshal into a map and back with MarshalIndent — Go maps have
// no defined iteration order, so encoding/json always emits map keys
// alphabetized, which would silently reorder every object's fields relative
// to how the client originally sent them. json.Indent operates on the
// token stream directly and preserves source order, matching Python's
// json.dumps(json.loads(...), indent=2), which preserves dict insertion
// order.
func prettyJSON(s *string) string {
	if s == nil {
		return ""
	}
	var buf bytes.Buffer
	if err := json.Indent(&buf, []byte(*s), "", "  "); err != nil {
		return *s
	}
	return buf.String()
}

// add is a small arithmetic helper for the dashboard's "total tokens" cells
// (input + output) and the exchanges list's page-1/page+1 pagination links,
// which Go templates can't compute inline on their own. Accepts int or
// int64 on either side (exchanges.html passes a literal -1/1 alongside an
// int Page field; dashboard.html passes two int64 totals).
func add(a, b any) int64 {
	av, _ := toInt64(a)
	bv, _ := toInt64(b)
	return av + bv
}

// FuncMap is the template.FuncMap shared by every admin HTML template.
func FuncMap() template.FuncMap {
	return template.FuncMap{
		"urlFor":     urlFor,
		"fmtCost":    fmtCost,
		"fmtInt":     fmtInt,
		"fmtTime":    fmtTime,
		"coalesce":   coalesce,
		"toJSON":     toJSON,
		"prettyJSON": prettyJSON,
		"add":        add,
	}
}
