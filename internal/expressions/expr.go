// Package expressions implements the GitHub Actions expression language.
//
// Supported: literals (including GitHub's signed, hex, octal, exponent and
// Infinity/NaN number forms), context lookups (a.b, a['b'], a[0]), the object
// filter syntax a.* (with chained-filter flattening), the operators !, ==, !=,
// <, <=, >, >=, &&, ||, parentheses, and the documented function set. GitHub
// has no arithmetic operators; + - * / outside number literals stay errors.
package expressions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// Interpolate expands every ${{ ... }} segment in tmpl using ctx.
func Interpolate(tmpl string, ctx map[string]any) (string, error) {
	if !strings.Contains(tmpl, "${{") {
		return tmpl, nil
	}
	var out strings.Builder
	rest := tmpl
	for {
		start := strings.Index(rest, "${{")
		if start < 0 {
			out.WriteString(rest)
			return out.String(), nil
		}
		out.WriteString(rest[:start])
		end := findClose(rest[start+3:])
		if end < 0 {
			return "", fmt.Errorf("unterminated expression in %q", tmpl)
		}
		body := rest[start+3 : start+3+end]
		v, err := Evaluate(body, ctx)
		if err != nil {
			return "", err
		}
		out.WriteString(Stringify(v))
		rest = rest[start+3+end+2:]
	}
}

// findClose returns the index of the closing "}}" that balances the expression.
func findClose(s string) int {
	inSingle, inDouble := false, false
	for i := 0; i < len(s)-1; i++ {
		switch s[i] {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '}':
			if !inSingle && !inDouble && s[i+1] == '}' {
				return i
			}
		}
	}
	return -1
}

// Evaluate parses and evaluates a single expression body (without ${{ }}).
func Evaluate(body string, ctx map[string]any) (any, error) {
	p, err := newParser(body)
	if err != nil {
		return nil, err
	}
	expr, err := p.parseExpression()
	if err != nil {
		return nil, err
	}
	if p.err != nil {
		return nil, p.err
	}
	if p.peek().kind != tokEOF {
		return nil, fmt.Errorf("unexpected token %q in expression", p.peek().text)
	}
	if p.err != nil {
		return nil, p.err
	}
	v, err := expr.eval(ctx)
	if err != nil {
		return nil, err
	}
	return unwrap(v), nil
}

// statusCheckRe detects an explicit status check function call. A preceding '.'
// disqualifies the match so property lookups don't read as functions.
var statusCheckRe = regexp.MustCompile(`(?i)(^|[^.\w])(success|failure|always|cancelled)\s*\(`)

// EvaluateCondition evaluates an `if:` condition. A condition may be wrapped in ${{ }}.
// GitHub applies an implicit success() when the expression names no status check
// function — that is what skips jobs and steps after a failure.
func EvaluateCondition(cond string, ctx map[string]any) (bool, error) {
	cond = strings.TrimSpace(cond)
	if strings.HasPrefix(cond, "${{") && strings.HasSuffix(cond, "}}") {
		cond = strings.TrimSpace(cond[3 : len(cond)-2])
	}
	if cond == "" {
		cond = "success()"
	} else if !statusCheckRe.MatchString(cond) {
		cond = "success() && (" + cond + ")"
	}
	v, err := Evaluate(cond, ctx)
	if err != nil {
		return false, err
	}
	return Truthy(v), nil
}

// Truthy mirrors GitHub's coercion: empty strings/zero/null are false.
func Truthy(v any) bool {
	switch t := v.(type) {
	case nil:
		return false
	case bool:
		return t
	case string:
		if t == "" {
			return false
		}
		f, err := strconv.ParseFloat(t, 64)
		if err != nil {
			return true
		}
		return f != 0
	case float64:
		return t != 0
	case int:
		return t != 0
	case int64:
		return t != 0
	default:
		return true
	}
}

// Stringify converts a value the way GitHub renders it in interpolations.
func Stringify(v any) string {
	switch t := v.(type) {
	case nil:
		return ""
	case string:
		return t
	case bool:
		return strconv.FormatBool(t)
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case int:
		return strconv.Itoa(t)
	case int64:
		return strconv.FormatInt(t, 10)
	case []any:
		return "Array"
	case map[string]any:
		return "Object"
	case filteredArray:
		return "Array"
	default:
		b, err := json.Marshal(t)
		if err != nil {
			return fmt.Sprint(t)
		}
		return string(b)
	}
}

// ---- lexer ----

type tokKind int

const (
	tokEOF tokKind = iota
	tokIdent
	tokNumber
	tokString
	tokOp
	tokPunct
)

type token struct {
	kind tokKind
	text string
}

type lexer struct {
	s   string
	pos int
}

func (l *lexer) next() (token, error) {
	for l.pos < len(l.s) && (l.s[l.pos] == ' ' || l.s[l.pos] == '\t' || l.s[l.pos] == '\n' || l.s[l.pos] == '\r') {
		l.pos++
	}
	if l.pos >= len(l.s) {
		return token{kind: tokEOF}, nil
	}
	c := l.s[l.pos]
	switch {
	case c == '\'' || c == '"':
		quote := c
		l.pos++
		var b strings.Builder
		for l.pos < len(l.s) {
			if l.s[l.pos] == quote {
				// Doubled quote is an escaped quote, as in GitHub expressions.
				if l.pos+1 < len(l.s) && l.s[l.pos+1] == quote {
					b.WriteByte(quote)
					l.pos += 2
					continue
				}
				l.pos++
				return token{kind: tokString, text: b.String()}, nil
			}
			if l.s[l.pos] == '\\' && l.pos+1 < len(l.s) && (l.s[l.pos+1] == '\\' || l.s[l.pos+1] == quote) {
				b.WriteByte(l.s[l.pos+1])
				l.pos += 2
				continue
			}
			b.WriteByte(l.s[l.pos])
			l.pos++
		}
		return token{}, fmt.Errorf("unterminated string literal")
	case isNumberStart(l.s, l.pos):
		start := l.pos
		l.scanNumber()
		return token{kind: tokNumber, text: l.s[start:l.pos]}, nil
	case isIdentStart(c):
		start := l.pos
		for l.pos < len(l.s) && isIdentPart(l.s[l.pos]) {
			l.pos++
		}
		return token{kind: tokIdent, text: l.s[start:l.pos]}, nil
	case strings.HasPrefix(l.s[l.pos:], "=="), strings.HasPrefix(l.s[l.pos:], "!="),
		strings.HasPrefix(l.s[l.pos:], "<="), strings.HasPrefix(l.s[l.pos:], ">="),
		strings.HasPrefix(l.s[l.pos:], "&&"), strings.HasPrefix(l.s[l.pos:], "||"):
		t := l.s[l.pos : l.pos+2]
		l.pos += 2
		return token{kind: tokOp, text: t}, nil
	case c == '!' || c == '<' || c == '>':
		l.pos++
		return token{kind: tokOp, text: string(c)}, nil
	case c == '(' || c == ')' || c == '[' || c == ']' || c == '.' || c == ',' || c == '*':
		l.pos++
		return token{kind: tokPunct, text: string(c)}, nil
	}
	return token{}, fmt.Errorf("unexpected character %q in expression", string(c))
}

func isIdentStart(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

func isHexDigit(c byte) bool {
	return isDigit(c) || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')
}

// isNumberStart reports whether s[pos] begins a GitHub number literal:
// digits, .digits, or a sign followed by either. GitHub has no +/- operators,
// so a sign directly before a digit is unambiguously part of the literal.
func isNumberStart(s string, pos int) bool {
	if pos >= len(s) {
		return false
	}
	c := s[pos]
	if isDigit(c) {
		return true
	}
	if c == '.' && pos+1 < len(s) && isDigit(s[pos+1]) {
		return true
	}
	if (c == '-' || c == '+') && pos+1 < len(s) {
		n := s[pos+1]
		return isDigit(n) || (n == '.' && pos+2 < len(s) && isDigit(s[pos+2])) ||
			strings.HasPrefix(s[pos+1:], "Infinity")
	}
	return false
}

// scanNumber consumes one GitHub number literal: [+-]? (0x hex | 0o octal |
// decimal with optional fraction and exponent). Decimal requires at least
// one digit on some side of the point; a malformed exponent is left for the
// next token so `1e` reads as `1` then an identifier, matching GitHub.
func (l *lexer) scanNumber() {
	if l.s[l.pos] == '-' || l.s[l.pos] == '+' {
		l.pos++
	}
	rest := l.s[l.pos:]
	// Signed Infinity is a literal; unsigned Infinity lexes as an identifier
	// handled in parsePrimary.
	if strings.HasPrefix(rest, "Infinity") {
		l.pos += len("Infinity")
		return
	}
	if strings.HasPrefix(rest, "0x") || strings.HasPrefix(rest, "0X") {
		j := 2
		for j < len(rest) && isHexDigit(rest[j]) {
			j++
		}
		if j > 2 {
			l.pos += j
			return
		}
	}
	if strings.HasPrefix(rest, "0o") || strings.HasPrefix(rest, "0O") {
		j := 2
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '7' {
			j++
		}
		if j > 2 {
			l.pos += j
			return
		}
	}
	for l.pos < len(l.s) && isDigit(l.s[l.pos]) {
		l.pos++
	}
	if l.pos < len(l.s) && l.s[l.pos] == '.' {
		l.pos++
		for l.pos < len(l.s) && isDigit(l.s[l.pos]) {
			l.pos++
		}
	}
	if l.pos < len(l.s) && (l.s[l.pos] == 'e' || l.s[l.pos] == 'E') {
		j := l.pos + 1
		if j < len(l.s) && (l.s[j] == '-' || l.s[j] == '+') {
			j++
		}
		k := j
		for k < len(l.s) && isDigit(l.s[k]) {
			k++
		}
		if k > j {
			l.pos = k
		}
	}
}

func isIdentPart(c byte) bool {
	// GitHub permits hyphens in context identifiers — inputs.foo-bar,
	// steps.my-step.outputs.x — and has no subtraction operator to clash.
	return isIdentStart(c) || (c >= '0' && c <= '9') || c == '-'
}

// ---- parser (precedence climbing) ----

type parser struct {
	lex    lexer
	buf    []token
	looked bool
	err    error
}

func newParser(s string) (*parser, error) {
	if strings.TrimSpace(s) == "" {
		return nil, fmt.Errorf("empty expression")
	}
	return &parser{lex: lexer{s: s}}, nil
}

func (p *parser) peek() token {
	if !p.looked {
		t, err := p.lex.next()
		if err != nil {
			p.err = err
			p.buf = []token{{kind: tokEOF}}
			p.looked = true
			return token{kind: tokEOF}
		}
		p.buf = append(p.buf[:0], t)
		p.looked = true
	}
	return p.buf[0]
}

func (p *parser) take() token {
	t := p.peek()
	p.looked = false
	return t
}

func (p *parser) parseExpression() (node, error) { return p.parseBinary(0) }

var precedence = map[string]int{
	"||": 1,
	"&&": 2,
	"==": 3, "!=": 3, "<": 3, "<=": 3, ">": 3, ">=": 3,
}

func (p *parser) parseBinary(minPrec int) (node, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		t := p.peek()
		if t.kind != tokOp {
			return left, nil
		}
		prec, ok := precedence[t.text]
		if !ok || prec < minPrec {
			return left, nil
		}
		op := p.take().text
		right, err := p.parseBinary(prec + 1)
		if err != nil {
			return nil, err
		}
		left = &binaryNode{op: op, left: left, right: right}
	}
}

func (p *parser) parseUnary() (node, error) {
	if t := p.peek(); t.kind == tokOp && t.text == "!" {
		p.take()
		inner, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return &notNode{inner: inner}, nil
	}
	primary, err := p.parsePrimary()
	if err != nil {
		return nil, err
	}
	return p.parsePostfix(primary)
}

func (p *parser) parsePostfix(base node) (node, error) {
	for {
		t := p.peek()
		if t.kind != tokPunct {
			return base, nil
		}
		switch t.text {
		case ".":
			p.take()
			name := p.take()
			switch name.kind {
			case tokIdent:
				base = &accessNode{base: base, key: &literalNode{value: name.text}}
			case tokPunct:
				if name.text != "*" {
					return nil, fmt.Errorf("expected property name after '.'")
				}
				base = &starNode{base: base}
			default:
				return nil, fmt.Errorf("expected property name after '.'")
			}
		case "[":
			p.take()
			key, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if end := p.take(); !(end.kind == tokPunct && end.text == "]") {
				return nil, fmt.Errorf("expected ']' in index access")
			}
			base = &accessNode{base: base, key: key}
		default:
			return base, nil
		}
	}
}

func (p *parser) parsePrimary() (node, error) {
	t := p.take()
	switch t.kind {
	case tokString:
		return &literalNode{value: t.text}, nil
	case tokNumber:
		f, err := parseNumberLiteral(t.text)
		if err != nil {
			return nil, fmt.Errorf("invalid number %q", t.text)
		}
		return &literalNode{value: f}, nil
	case tokIdent:
		switch t.text {
		case "true":
			return &literalNode{value: true}, nil
		case "false":
			return &literalNode{value: false}, nil
		case "null":
			return &literalNode{value: nil}, nil
		case "NaN":
			return &literalNode{value: math.NaN()}, nil
		case "Infinity":
			return &literalNode{value: math.Inf(1)}, nil
		}
		if p.peek().kind == tokPunct && p.peek().text == "(" {
			p.take()
			var args []node
			if !(p.peek().kind == tokPunct && p.peek().text == ")") {
				for {
					arg, err := p.parseExpression()
					if err != nil {
						return nil, err
					}
					args = append(args, arg)
					if p.peek().kind == tokPunct && p.peek().text == "," {
						p.take()
						continue
					}
					break
				}
			}
			if end := p.take(); !(end.kind == tokPunct && end.text == ")") {
				return nil, fmt.Errorf("expected ')' after arguments to %s", t.text)
			}
			return &callNode{name: t.text, args: args}, nil
		}
		return &identNode{path: t.text}, nil
	case tokPunct:
		if t.text == "(" {
			inner, err := p.parseExpression()
			if err != nil {
				return nil, err
			}
			if end := p.take(); !(end.kind == tokPunct && end.text == ")") {
				return nil, fmt.Errorf("expected ')'")
			}
			return inner, nil
		}
	}
	return nil, fmt.Errorf("unexpected token %q in expression", t.text)
}

// ---- AST ----

type node interface {
	eval(ctx map[string]any) (any, error)
}

type literalNode struct{ value any }

func (n *literalNode) eval(map[string]any) (any, error) { return n.value, nil }

type identNode struct{ path string }

func (n *identNode) eval(ctx map[string]any) (any, error) {
	v, _ := lookup(ctx, n.path)
	return v, nil
}

type accessNode struct {
	base node
	key  node
}

func (n *accessNode) eval(ctx map[string]any) (any, error) {
	raw, err := n.base.eval(ctx)
	if err != nil {
		return nil, err
	}
	keyVal, err := n.key.eval(ctx)
	if err != nil {
		return nil, err
	}
	key := Stringify(unwrap(keyVal))
	// A filter result dereferences per item; items missing the key are
	// skipped rather than null-filled, matching GitHub's FilteredArray.
	// The marker must be checked before unwrap() erases it.
	if fa, ok := raw.(filteredArray); ok {
		out := filteredArray{}
		for _, item := range fa.items {
			if v, found := accessOne(item, key); found {
				out.items = append(out.items, v)
			}
		}
		return out, nil
	}
	base := unwrap(raw)
	v, _ := accessOne(base, key)
	return v, nil
}

// accessOne resolves key against a single value. found is false when the
// value is not a collection or lacks the key — the filtered-chain skip rule.
func accessOne(base any, key string) (any, bool) {
	switch m := base.(type) {
	case map[string]any:
		v, ok := m[key]
		return v, ok
	case []any:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(m) {
			return nil, false
		}
		return m[i], true
	}
	return nil, false
}

// filteredArray is the result of an object filter (a.*). It stays wrapped
// through chained dereferences so missing keys skip instead of null-fill,
// and unwraps to a plain []any at expression boundaries.
type filteredArray struct{ items []any }

// ListElements reports whether v is a list value — a plain array or a
// filter result — and returns its elements.
func ListElements(v any) ([]any, bool) {
	switch t := v.(type) {
	case []any:
		return t, true
	case filteredArray:
		return t.items, true
	}
	return nil, false
}

func unwrap(v any) any {
	if f, ok := v.(filteredArray); ok {
		return f.items
	}
	return v
}

// appendWildcard expands one value the way GitHub's `.*` dereference does:
// object values (key-sorted for determinism — Go maps lose YAML order) or
// array elements. Primitives contribute nothing.
func appendWildcard(out []any, v any) []any {
	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			out = append(out, t[k])
		}
	case []any:
		out = append(out, t...)
	}
	return out
}

type starNode struct{ base node }

func (n *starNode) eval(ctx map[string]any) (any, error) {
	raw, err := n.base.eval(ctx)
	if err != nil {
		return nil, err
	}
	// Chained filters flatten one level: each prior item expands by `.*`.
	// Check the marker before unwrap() erases it.
	if fa, ok := raw.(filteredArray); ok {
		var out []any
		for _, item := range fa.items {
			out = appendWildcard(out, unwrap(item))
		}
		return filteredArray{items: out}, nil
	}
	var out []any
	out = appendWildcard(out, unwrap(raw))
	return filteredArray{items: out}, nil
}

type notNode struct{ inner node }

func (n *notNode) eval(ctx map[string]any) (any, error) {
	v, err := n.inner.eval(ctx)
	if err != nil {
		return nil, err
	}
	return !Truthy(unwrap(v)), nil
}

type binaryNode struct {
	op          string
	left, right node
}

func (n *binaryNode) eval(ctx map[string]any) (any, error) {
	// Short-circuit before evaluating the right side.
	switch n.op {
	case "&&":
		l, err := n.left.eval(ctx)
		if err != nil {
			return nil, err
		}
		if !Truthy(l) {
			return l, nil
		}
		return n.right.eval(ctx)
	case "||":
		l, err := n.left.eval(ctx)
		if err != nil {
			return nil, err
		}
		if Truthy(l) {
			return l, nil
		}
		return n.right.eval(ctx)
	}
	l, err := n.left.eval(ctx)
	if err != nil {
		return nil, err
	}
	r, err := n.right.eval(ctx)
	if err != nil {
		return nil, err
	}
	l, r = unwrap(l), unwrap(r)
	switch n.op {
	case "==":
		return equalValues(l, r), nil
	case "!=":
		return !equalValues(l, r), nil
	case "<", "<=", ">", ">=":
		return compareValues(n.op, l, r), nil
	}
	return nil, fmt.Errorf("unsupported operator %q", n.op)
}

type callNode struct {
	name string
	args []node
}

func (n *callNode) eval(ctx map[string]any) (any, error) {
	args := make([]any, 0, len(n.args))
	for _, a := range n.args {
		v, err := a.eval(ctx)
		if err != nil {
			return nil, err
		}
		args = append(args, unwrap(v))
	}
	return call(n.name, args, ctx)
}

func lookup(ctx map[string]any, path string) (any, bool) {
	parts := strings.Split(path, ".")
	var cur any = ctx
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = m[p]
		if !ok {
			return nil, false
		}
	}
	return cur, true
}

// valueKind mirrors GitHub's ValueKind for AbstractEqual coercion.
type valueKind int

const (
	kindNull valueKind = iota
	kindBool
	kindNumber
	kindString
	kindArray
	kindObject
)

func kindOf(v any) valueKind {
	switch v.(type) {
	case nil:
		return kindNull
	case bool:
		return kindBool
	case float64, int, int64:
		return kindNumber
	case string:
		return kindString
	case []any:
		return kindArray
	case map[string]any:
		return kindObject
	}
	return kindNull
}

func numValue(v any) float64 {
	switch t := v.(type) {
	case float64:
		return t
	case int:
		return float64(t)
	case int64:
		return float64(t)
	}
	return math.NaN()
}

// coerceForCompare ports GitHub's CoerceTypes: numbers and strings coerce
// across, booleans and null coerce to numbers and recurse. Arrays and
// objects never coerce — mismatched kinds simply never compare equal.
func coerceForCompare(a, b any) (any, any) {
	ka, kb := kindOf(a), kindOf(b)
	if ka == kb {
		return a, b
	}
	switch {
	case ka == kindNumber && kb == kindString:
		return a, parseNumberValue(Stringify(b))
	case ka == kindString && kb == kindNumber:
		return parseNumberValue(Stringify(a)), b
	case ka == kindBool || ka == kindNull:
		return coerceForCompare(boolNullToNumber(a), b)
	case kb == kindBool || kb == kindNull:
		return coerceForCompare(a, boolNullToNumber(b))
	}
	return a, b
}

func boolNullToNumber(v any) float64 {
	if b, ok := v.(bool); ok && b {
		return 1
	}
	return 0
}

// equalValues ports GitHub's AbstractEqual: same-kind comparisons only after
// CoerceTypes. Strings compare OrdinalIgnoreCase; arrays and objects are
// equal only by identity, so two distinct collections are never equal.
func equalValues(a, b any) bool {
	a, b = coerceForCompare(unwrap(a), unwrap(b))
	ka, kb := kindOf(a), kindOf(b)
	if ka != kb {
		return false
	}
	switch ka {
	case kindNull:
		return true
	case kindNumber:
		af, bf := numValue(a), numValue(b)
		if math.IsNaN(af) || math.IsNaN(bf) {
			return false
		}
		return af == bf
	case kindString:
		return strings.EqualFold(a.(string), b.(string))
	case kindBool:
		return a.(bool) == b.(bool)
	}
	return false
}

// compareValues ports GitHub's AbstractLessThan/GreaterThan family after the
// same coercion: numbers compare numerically, strings OrdinalIgnoreCase
// lexicographically, booleans order false < true. <=/>= are equal-or-ordered,
// and NaN never satisfies any comparison.
func compareValues(op string, a, b any) bool {
	a, b = coerceForCompare(unwrap(a), unwrap(b))
	ka, kb := kindOf(a), kindOf(b)
	if ka != kb {
		return false
	}
	var cmp int
	switch ka {
	case kindNumber:
		af, bf := numValue(a), numValue(b)
		if math.IsNaN(af) || math.IsNaN(bf) {
			return false
		}
		switch {
		case af < bf:
			cmp = -1
		case af > bf:
			cmp = 1
		}
	case kindString:
		cmp = strings.Compare(strings.ToLower(a.(string)), strings.ToLower(b.(string)))
	case kindBool:
		// GitHub orders booleans false < true.
		ai, bi := 0, 0
		if a.(bool) {
			ai = 1
		}
		if b.(bool) {
			bi = 1
		}
		switch {
		case ai < bi:
			cmp = -1
		case ai > bi:
			cmp = 1
		}
	default:
		return false
	}
	switch op {
	case "<":
		return cmp < 0
	case "<=":
		return cmp <= 0
	case ">":
		return cmp > 0
	case ">=":
		return cmp >= 0
	}
	return false
}

// parseNumberValue ports GitHub's ExpressionUtility.ParseNumber: trim,
// empty → 0, .NET-style decimal (sign, point, exponent), then lowercase
// 0x hex and 0o octal bounded to Int32, then the exact strings "Infinity"
// and "-Infinity". Everything else — including "nan"/"inf" spellings Go
// would accept — is NaN.
func parseNumberValue(s string) float64 {
	t := strings.TrimSpace(s)
	if t == "" {
		return 0
	}
	if f, ok := parseDecimal(t); ok {
		return f
	}
	if len(t) > 2 && (t[:2] == "0x" || t[:2] == "0X") {
		if v, err := strconv.ParseUint(t[2:], 16, 32); err == nil {
			return float64(v)
		}
		return math.NaN()
	}
	if len(t) > 2 && (t[:2] == "0o" || t[:2] == "0O") {
		if v, err := strconv.ParseUint(t[2:], 8, 32); err == nil {
			return float64(v)
		}
		return math.NaN()
	}
	switch t {
	case "Infinity":
		return math.Inf(1)
	case "-Infinity":
		return math.Inf(-1)
	}
	return math.NaN()
}

var decimalRe = regexp.MustCompile(`^[+-]?([0-9]+\.?[0-9]*|\.[0-9]+)([eE][+-]?[0-9]+)?$`)

func parseDecimal(s string) (float64, bool) {
	if !decimalRe.MatchString(s) {
		return 0, false
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return f, true
}

// parseNumberLiteral converts a lexer number token. The lexer guarantees the
// shape; hex and octal literals are unsigned magnitudes with the sign applied
// after, matching GitHub's double-valued results.
func parseNumberLiteral(s string) (float64, error) {
	neg := false
	t := s
	if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "+") {
		neg = t[0] == '-'
		t = t[1:]
	}
	var f float64
	var err error
	switch {
	case len(t) > 2 && (t[:2] == "0x" || t[:2] == "0X"):
		var v uint64
		v, err = strconv.ParseUint(t[2:], 16, 64)
		f = float64(v)
	case len(t) > 2 && (t[:2] == "0o" || t[:2] == "0O"):
		var v uint64
		v, err = strconv.ParseUint(t[2:], 8, 64)
		f = float64(v)
	default:
		f, err = strconv.ParseFloat(t, 64)
	}
	if err != nil {
		return 0, err
	}
	if neg {
		f = -f
	}
	return f, nil
}

func call(name string, args []any, ctx map[string]any) (any, error) {
	switch strings.ToLower(name) {
	case "contains":
		if len(args) != 2 {
			return nil, fmt.Errorf("contains expects 2 arguments")
		}
		// GitHub: an array search uses AbstractEqual per item; a primitive
		// search is a case-insensitive substring on the string form; objects
		// never contain anything.
		switch search := args[0].(type) {
		case []any:
			for _, item := range search {
				if equalValues(item, args[1]) {
					return true, nil
				}
			}
			return false, nil
		case map[string]any:
			return false, nil
		default:
			return strings.Contains(
				strings.ToLower(Stringify(search)),
				strings.ToLower(Stringify(args[1]))), nil
		}
	case "startswith":
		if len(args) != 2 {
			return nil, fmt.Errorf("startsWith expects 2 arguments")
		}
		return strings.HasPrefix(strings.ToLower(Stringify(args[0])), strings.ToLower(Stringify(args[1]))), nil
	case "endswith":
		if len(args) != 2 {
			return nil, fmt.Errorf("endsWith expects 2 arguments")
		}
		return strings.HasSuffix(strings.ToLower(Stringify(args[0])), strings.ToLower(Stringify(args[1]))), nil
	case "format":
		if len(args) < 1 {
			return nil, fmt.Errorf("format expects at least 1 argument")
		}
		return formatString(Stringify(args[0]), args[1:])
	case "join":
		if len(args) == 0 {
			return nil, fmt.Errorf("join expects at least 1 argument")
		}
		sep := ","
		if len(args) > 1 {
			sep = Stringify(args[1])
		}
		// GitHub join(): arrays stringify each primitive element; a primitive
		// input returns its own string form; other collections give "".
		switch items := args[0].(type) {
		case []any:
			parts := make([]string, len(items))
			for i, item := range items {
				parts[i] = Stringify(item)
			}
			return strings.Join(parts, sep), nil
		case map[string]any:
			return "", nil
		default:
			return Stringify(args[0]), nil
		}
	case "tojson":
		if len(args) != 1 {
			return nil, fmt.Errorf("toJSON expects 1 argument")
		}
		// GitHub pretty-prints with indentation; keep HTML chars literal so
		// URLs and comparisons survive the round trip.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(args[0]); err != nil {
			return nil, err
		}
		return strings.TrimRight(buf.String(), "\n"), nil
	case "case":
		// GitHub's undocumented case(): odd arg count, (predicate, result)
		// pairs and a trailing default. Predicates must be real booleans.
		if len(args)%2 == 0 {
			return nil, fmt.Errorf("case expects an odd number of arguments")
		}
		for i := 0; i+1 < len(args); i += 2 {
			pred, ok := args[i].(bool)
			if !ok {
				return nil, fmt.Errorf("case predicate %d must evaluate to a boolean", i+1)
			}
			if pred {
				return args[i+1], nil
			}
		}
		return args[len(args)-1], nil
	case "fromjson":
		if len(args) != 1 {
			return nil, fmt.Errorf("fromJSON expects 1 argument")
		}
		var v any
		if err := json.Unmarshal([]byte(Stringify(args[0])), &v); err != nil {
			return nil, fmt.Errorf("fromJSON: %w", err)
		}
		return normalizeJSON(v), nil
	case "hashfiles":
		ws, _ := ctx["__workspace"].(string)
		if ws == "" {
			return nil, fmt.Errorf("hashFiles is only available in step-level expressions running on a runner (not in job if:, outputs evaluated server-side, or other contexts without a workspace)")
		}
		return hashFilesFunc(ws, args)
	case "success", "failure", "always", "cancelled":
		status, _ := ctx["__job_status"].(string)
		switch strings.ToLower(name) {
		case "success":
			return status == "" || status == "success", nil
		case "failure":
			return status == "failure", nil
		case "cancelled":
			return status == "cancelled", nil
		default:
			return true, nil
		}
	}
	return nil, fmt.Errorf("unknown function %q", name)
}

// hashFilesFunc hashes every file matched by the glob patterns under ws —
// sha256 over the sorted per-file content hashes, GitHub-compatible enough for
// "did the lockfile change" cache keys.
func hashFilesFunc(ws string, args []any) (any, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("hashFiles expects at least 1 pattern")
	}
	seen := map[string]bool{}
	var files []string
	for _, a := range args {
		pat := Stringify(a)
		matches, err := doublestar.FilepathGlob(filepath.Join(ws, filepath.FromSlash(pat)))
		if err != nil {
			return nil, fmt.Errorf("hashFiles: invalid pattern %q: %w", pat, err)
		}
		for _, m := range matches {
			if info, err := os.Stat(m); err == nil && info.Mode().IsRegular() && !seen[m] {
				seen[m] = true
				files = append(files, m)
			}
		}
	}
	sort.Strings(files)
	h := sha256.New()
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		sum := sha256.Sum256(data)
		h.Write(sum[:])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// normalizeJSON converts encoding/json numbers into the evaluator's numeric type.
func normalizeJSON(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, val := range t {
			t[k] = normalizeJSON(val)
		}
		return t
	case []any:
		for i, val := range t {
			t[i] = normalizeJSON(val)
		}
		return t
	case json.Number:
		f, _ := t.Float64()
		return f
	default:
		return v
	}
}

func formatString(f string, args []any) (string, error) {
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] == '{' {
			if i+1 < len(f) && f[i+1] == '{' {
				b.WriteByte('{')
				i++
				continue
			}
			end := strings.IndexByte(f[i:], '}')
			if end < 0 {
				return "", fmt.Errorf("unterminated placeholder in format string")
			}
			idxStr := f[i+1 : i+end]
			idx, err := strconv.Atoi(strings.TrimSpace(idxStr))
			if err != nil {
				return "", fmt.Errorf("invalid placeholder {%s}", idxStr)
			}
			if idx < 0 || idx >= len(args) {
				return "", fmt.Errorf("format placeholder {%d} out of range", idx)
			}
			b.WriteString(Stringify(args[idx]))
			i += end
			continue
		}
		if f[i] == '}' && i+1 < len(f) && f[i+1] == '}' {
			b.WriteByte('}')
			i++
			continue
		}
		b.WriteByte(f[i])
	}
	return b.String(), nil
}
