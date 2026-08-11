package database

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file implements the small query language used by the Exchanges list
// page's search box, e.g.:
//
//	model like sonnet AND cost > 0.5
//	(session = "abc-123" OR session = "def-456") AND date >= 2026-08-01
//
// Grammar (AND binds tighter than OR, matching SQL; parens override that):
//
//	query   := orExpr
//	orExpr  := andExpr (OR andExpr)*
//	andExpr := term (AND term)*
//	term    := cond | '(' orExpr ')'
//	cond    := FIELD OP VALUE
//	VALUE   := quoted string ("..."/'...') | bare token (no spaces/keywords)
//
// Compile() turns a query string into a parameterized SQL WHERE fragment and
// its bound args — every value is passed as a '?' placeholder, never string-
// concatenated, so arbitrary user input can't reshape the query.

// totalTokensExpr and cacheTokensExpr are the shared SQL expressions for the
// "total_tokens"/"cache_tokens" filter fields, matching the NULL-safe
// total_tokens column computed in GetExchanges (a plain "+" across nullable
// columns would collapse to NULL if any single column — e.g. a pre-cache-
// tracking historical row's cache_creation_tokens — is NULL).
const totalTokensExpr = `CASE WHEN input_tokens IS NULL AND output_tokens IS NULL
                               AND cache_creation_tokens IS NULL AND cache_read_tokens IS NULL
                          THEN NULL
                          ELSE COALESCE(input_tokens, 0) + COALESCE(output_tokens, 0)
                               + COALESCE(cache_creation_tokens, 0) + COALESCE(cache_read_tokens, 0)
                     END`

const cacheTokensExpr = `(COALESCE(cache_creation_tokens, 0) + COALESCE(cache_read_tokens, 0))`

type filterFieldKind int

const (
	fieldText filterFieldKind = iota
	fieldNumber
	fieldDate
	fieldBool
)

// filterField describes one FIELD name accepted by the query language: which
// SQL expression(s) it reads from (more than one for "session", which
// matches either session_id or session_name), its value kind, and which
// operators are valid for it.
type filterField struct {
	kind  filterFieldKind
	exprs []string
}

var filterFields = map[string]filterField{
	"id":            {kind: fieldNumber, exprs: []string{"id"}},
	"session":       {kind: fieldText, exprs: []string{"session_id", "session_name"}},
	"model":         {kind: fieldText, exprs: []string{"model"}},
	"path":          {kind: fieldText, exprs: []string{"path"}},
	"date":          {kind: fieldDate, exprs: []string{"timestamp"}},
	"input_tokens":  {kind: fieldNumber, exprs: []string{"input_tokens"}},
	"output_tokens": {kind: fieldNumber, exprs: []string{"output_tokens"}},
	"cache_tokens":  {kind: fieldNumber, exprs: []string{cacheTokensExpr}},
	"total_tokens":  {kind: fieldNumber, exprs: []string{totalTokensExpr}},
	"cost":          {kind: fieldNumber, exprs: []string{"cost"}},
	"stream":        {kind: fieldBool, exprs: []string{"is_streaming"}},
}

var textOps = map[string]bool{"=": true, "like": true}
var scalarOps = map[string]bool{"=": true, "!=": true, "<": true, ">": true, "<=": true, ">=": true}
var boolOps = map[string]bool{"=": true}

func allowedOps(kind filterFieldKind) map[string]bool {
	switch kind {
	case fieldText:
		return textOps
	case fieldBool:
		return boolOps
	default:
		return scalarOps
	}
}

// ---- AST -------------------------------------------------------------

type filterExpr interface{ isFilterExpr() }

type filterAnd struct{ terms []filterExpr }
type filterOr struct{ terms []filterExpr }
type filterCond struct {
	field, op, value string
	pos              int
}

func (*filterAnd) isFilterExpr()  {}
func (*filterOr) isFilterExpr()   {}
func (*filterCond) isFilterExpr() {}

// ---- Lexer -------------------------------------------------------------

type filterTokenKind int

const (
	ftEOF filterTokenKind = iota
	ftLParen
	ftRParen
	ftAnd
	ftOr
	ftLike
	ftOp
	ftIdent
)

type filterToken struct {
	kind filterTokenKind
	text string
	pos  int
}

func lexFilterQuery(q string) ([]filterToken, error) {
	runes := []rune(q)
	var toks []filterToken
	i := 0
	for i < len(runes) {
		r := runes[i]
		switch {
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			i++
		case r == '(':
			toks = append(toks, filterToken{ftLParen, "(", i})
			i++
		case r == ')':
			toks = append(toks, filterToken{ftRParen, ")", i})
			i++
		case r == '"' || r == '\'':
			start := i
			quote := r
			i++
			var sb strings.Builder
			closed := false
			for i < len(runes) {
				c := runes[i]
				if c == '\\' && i+1 < len(runes) && (runes[i+1] == quote || runes[i+1] == '\\') {
					sb.WriteRune(runes[i+1])
					i += 2
					continue
				}
				if c == quote {
					closed = true
					i++
					break
				}
				sb.WriteRune(c)
				i++
			}
			if !closed {
				return nil, fmt.Errorf("unterminated string starting at position %d", start+1)
			}
			toks = append(toks, filterToken{ftIdent, sb.String(), start})
		case r == '=':
			toks = append(toks, filterToken{ftOp, "=", i})
			i++
		case r == '!':
			if i+1 < len(runes) && runes[i+1] == '=' {
				toks = append(toks, filterToken{ftOp, "!=", i})
				i += 2
			} else {
				return nil, fmt.Errorf("expected '=' after '!' at position %d", i+1)
			}
		case r == '<':
			if i+1 < len(runes) && runes[i+1] == '=' {
				toks = append(toks, filterToken{ftOp, "<=", i})
				i += 2
			} else {
				toks = append(toks, filterToken{ftOp, "<", i})
				i++
			}
		case r == '>':
			if i+1 < len(runes) && runes[i+1] == '=' {
				toks = append(toks, filterToken{ftOp, ">=", i})
				i += 2
			} else {
				toks = append(toks, filterToken{ftOp, ">", i})
				i++
			}
		default:
			start := i
			for i < len(runes) {
				c := runes[i]
				if c == ' ' || c == '\t' || c == '\n' || c == '\r' ||
					c == '(' || c == ')' || c == '"' || c == '\'' ||
					c == '=' || c == '!' || c == '<' || c == '>' {
					break
				}
				i++
			}
			word := string(runes[start:i])
			switch strings.ToLower(word) {
			case "and":
				toks = append(toks, filterToken{ftAnd, word, start})
			case "or":
				toks = append(toks, filterToken{ftOr, word, start})
			case "like":
				toks = append(toks, filterToken{ftLike, word, start})
			default:
				toks = append(toks, filterToken{ftIdent, word, start})
			}
		}
	}
	return toks, nil
}

// ---- Parser -------------------------------------------------------------

type filterParser struct {
	toks []filterToken
	pos  int
}

func (p *filterParser) peek() filterToken {
	if p.pos >= len(p.toks) {
		return filterToken{kind: ftEOF, pos: -1}
	}
	return p.toks[p.pos]
}

func (p *filterParser) next() filterToken {
	t := p.peek()
	if p.pos < len(p.toks) {
		p.pos++
	}
	return t
}

func (p *filterParser) parseOr() (filterExpr, error) {
	first, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	terms := []filterExpr{first}
	for p.peek().kind == ftOr {
		p.next()
		next, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		terms = append(terms, next)
	}
	if len(terms) == 1 {
		return terms[0], nil
	}
	return &filterOr{terms: terms}, nil
}

func (p *filterParser) parseAnd() (filterExpr, error) {
	first, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	terms := []filterExpr{first}
	for p.peek().kind == ftAnd {
		p.next()
		next, err := p.parseTerm()
		if err != nil {
			return nil, err
		}
		terms = append(terms, next)
	}
	if len(terms) == 1 {
		return terms[0], nil
	}
	return &filterAnd{terms: terms}, nil
}

func (p *filterParser) parseTerm() (filterExpr, error) {
	if p.peek().kind == ftLParen {
		p.next()
		inner, err := p.parseOr()
		if err != nil {
			return nil, err
		}
		closing := p.next()
		if closing.kind != ftRParen {
			return nil, fmt.Errorf("expected closing ')' at position %d", closing.pos+1)
		}
		return inner, nil
	}
	return p.parseCond()
}

func (p *filterParser) parseCond() (filterExpr, error) {
	fieldTok := p.next()
	if fieldTok.kind != ftIdent {
		return nil, fmt.Errorf("expected a field name at position %d, got %q", fieldTok.pos+1, fieldTok.text)
	}

	opTok := p.next()
	if opTok.kind != ftOp && opTok.kind != ftLike {
		return nil, fmt.Errorf("expected an operator after %q at position %d", fieldTok.text, opTok.pos+1)
	}

	valTok := p.next()
	if valTok.kind != ftIdent {
		return nil, fmt.Errorf("expected a value after %q %q at position %d", fieldTok.text, opTok.text, valTok.pos+1)
	}

	op := opTok.text
	if opTok.kind == ftLike {
		op = "like"
	}
	return &filterCond{field: fieldTok.text, op: strings.ToLower(op), value: valTok.text, pos: fieldTok.pos}, nil
}

// ---- Value parsing -------------------------------------------------------

// filterDateLayouts are tried in order; dates are interpreted in local time,
// matching fmtTime's rendering of the timestamp column.
var filterDateLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
	"2006-01-02",
}

func parseFilterDate(raw string) (float64, error) {
	for _, layout := range filterDateLayouts {
		if t, err := time.ParseInLocation(layout, raw, time.Local); err == nil {
			return float64(t.Unix()), nil
		}
	}
	return 0, fmt.Errorf("expected a date like YYYY-MM-DD or \"YYYY-MM-DD HH:MM:SS\", got %q", raw)
}

func parseFilterBool(raw string) (bool, error) {
	switch strings.ToLower(raw) {
	case "true", "1", "yes":
		return true, nil
	case "false", "0", "no":
		return false, nil
	default:
		return false, fmt.Errorf("expected true/false, got %q", raw)
	}
}

// escapeLikeValue escapes SQLite LIKE wildcards in user-provided text so a
// "like" filter searches for the literal characters "%"/"_" rather than
// letting them act as wildcards.
func escapeLikeValue(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "%", "\\%")
	s = strings.ReplaceAll(s, "_", "\\_")
	return s
}

// ---- Compiling AST -> SQL -------------------------------------------------

func compileFilterCond(c *filterCond) (string, []any, error) {
	field, ok := filterFields[strings.ToLower(c.field)]
	if !ok {
		return "", nil, fmt.Errorf("unknown field %q at position %d", c.field, c.pos+1)
	}
	if !allowedOps(field.kind)[c.op] {
		return "", nil, fmt.Errorf("field %q does not support operator %q", c.field, c.op)
	}

	switch field.kind {
	case fieldText:
		sqlOp := "="
		val := c.value
		suffix := ""
		if c.op == "like" {
			sqlOp = "LIKE"
			val = "%" + escapeLikeValue(c.value) + "%"
			suffix = " ESCAPE '\\'"
		}
		var parts []string
		var args []any
		for _, expr := range field.exprs {
			parts = append(parts, expr+" "+sqlOp+" ?"+suffix)
			args = append(args, val)
		}
		if len(parts) == 1 {
			return parts[0], args, nil
		}
		return "(" + strings.Join(parts, " OR ") + ")", args, nil

	case fieldNumber:
		v, err := strconv.ParseFloat(c.value, 64)
		if err != nil {
			return "", nil, fmt.Errorf("field %q expects a number, got %q", c.field, c.value)
		}
		return field.exprs[0] + " " + c.op + " ?", []any{v}, nil

	case fieldDate:
		v, err := parseFilterDate(c.value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q: %w", c.field, err)
		}
		return field.exprs[0] + " " + c.op + " ?", []any{v}, nil

	case fieldBool:
		v, err := parseFilterBool(c.value)
		if err != nil {
			return "", nil, fmt.Errorf("field %q: %w", c.field, err)
		}
		arg := 0
		if v {
			arg = 1
		}
		return field.exprs[0] + " = ?", []any{arg}, nil
	}
	return "", nil, fmt.Errorf("unhandled field kind for %q", c.field)
}

func compileFilterList(terms []filterExpr, joiner string) (string, []any, error) {
	var parts []string
	var args []any
	for _, t := range terms {
		frag, a, err := compileFilterExpr(t)
		if err != nil {
			return "", nil, err
		}
		if _, isCond := t.(*filterCond); !isCond {
			frag = "(" + frag + ")"
		}
		parts = append(parts, frag)
		args = append(args, a...)
	}
	return strings.Join(parts, " "+joiner+" "), args, nil
}

func compileFilterExpr(e filterExpr) (string, []any, error) {
	switch n := e.(type) {
	case *filterCond:
		return compileFilterCond(n)
	case *filterAnd:
		return compileFilterList(n.terms, "AND")
	case *filterOr:
		return compileFilterList(n.terms, "OR")
	default:
		return "", nil, fmt.Errorf("unhandled expression type %T", e)
	}
}

// parseFilterQuery is the shared entry point for both Compile and
// ExtractExactSession: it lexes/parses q and, for a blank query, reports
// (nil, nil) rather than an error so callers can treat "no filter" as a
// distinct, valid case.
func parseFilterQuery(q string) (filterExpr, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	toks, err := lexFilterQuery(q)
	if err != nil {
		return nil, err
	}
	p := &filterParser{toks: toks}
	expr, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.pos < len(p.toks) {
		tok := p.toks[p.pos]
		return nil, fmt.Errorf("unexpected %q at position %d", tok.text, tok.pos+1)
	}
	return expr, nil
}

// CompileExchangeFilter turns a search-box query into a parameterized SQL
// WHERE fragment (no leading "WHERE") and its bound args. An empty/blank
// query compiles to ("", nil, nil), meaning "no filter".
func CompileExchangeFilter(q string) (string, []any, error) {
	expr, err := parseFilterQuery(q)
	if err != nil {
		return "", nil, err
	}
	if expr == nil {
		return "", nil, nil
	}
	return compileFilterExpr(expr)
}

// ExtractExactSession reports whether q is exactly one top-level
// `session = "..."` condition (no AND/OR, no "like"), returning the session
// id/name value if so. Used to decide whether the Exchanges page's "Clear
// session" (vs. "Clear all") delete action applies — deliberately narrow so
// a multi-condition filter never turns into an implicit "delete everything
// matching this filter" action.
func ExtractExactSession(q string) (string, bool) {
	expr, err := parseFilterQuery(q)
	if err != nil || expr == nil {
		return "", false
	}
	cond, ok := expr.(*filterCond)
	if !ok {
		return "", false
	}
	if strings.ToLower(cond.field) != "session" || cond.op != "=" {
		return "", false
	}
	return cond.value, true
}
