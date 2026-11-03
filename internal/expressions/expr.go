// Package expressions implements the GitHub Actions expression language.
//
// Supported: literals, context lookups (a.b, a['b'], a[0]), the operators !, ==, !=,
// <, <=, >, >=, &&, ||, parentheses, and the core function set. Arithmetic and the
// object filter syntax are intentionally out of scope and produce errors.
package expressions

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
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
	return expr.eval(ctx)
}

// EvaluateCondition evaluates an `if:` condition. A condition may be wrapped in ${{ }}.
func EvaluateCondition(cond string, ctx map[string]any) (bool, error) {
	cond = strings.TrimSpace(cond)
	if cond == "" {
		return true, nil
	}
	if strings.HasPrefix(cond, "${{") && strings.HasSuffix(cond, "}}") {
		cond = strings.TrimSpace(cond[3 : len(cond)-2])
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
	case c >= '0' && c <= '9':
		start := l.pos
		for l.pos < len(l.s) && (l.s[l.pos] >= '0' && l.s[l.pos] <= '9') {
			l.pos++
		}
		if l.pos < len(l.s) && l.s[l.pos] == '.' {
			l.pos++
			for l.pos < len(l.s) && (l.s[l.pos] >= '0' && l.s[l.pos] <= '9') {
				l.pos++
			}
		}
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

func isIdentPart(c byte) bool {
	return isIdentStart(c) || (c >= '0' && c <= '9')
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
		f, err := strconv.ParseFloat(t.text, 64)
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
	base, err := n.base.eval(ctx)
	if err != nil {
		return nil, err
	}
	keyVal, err := n.key.eval(ctx)
	if err != nil {
		return nil, err
	}
	key := Stringify(keyVal)
	switch m := base.(type) {
	case map[string]any:
		return m[key], nil
	case []any:
		i, err := strconv.Atoi(key)
		if err != nil || i < 0 || i >= len(m) {
			return nil, nil
		}
		return m[i], nil
	default:
		return nil, nil
	}
}

type starNode struct{ base node }

func (n *starNode) eval(ctx map[string]any) (any, error) { return nil, nil }

type notNode struct{ inner node }

func (n *notNode) eval(ctx map[string]any) (any, error) {
	v, err := n.inner.eval(ctx)
	if err != nil {
		return nil, err
	}
	return !Truthy(v), nil
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
		args = append(args, v)
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

func equalValues(a, b any) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	af, aNum := toNumber(a)
	bf, bNum := toNumber(b)
	if aNum && bNum {
		return af == bf
	}
	as, aStr := a.(string)
	bs, bStr := b.(string)
	if aStr && bStr {
		if af, err := strconv.ParseFloat(as, 64); err == nil {
			if bf, err := strconv.ParseFloat(bs, 64); err == nil {
				return af == bf
			}
		}
		return strings.EqualFold(as, bs)
	}
	if ab, ok := a.(bool); ok {
		if bb, ok := b.(bool); ok {
			return ab == bb
		}
	}
	return Stringify(a) == Stringify(b)
}

func compareValues(op string, a, b any) bool {
	af, aNum := toNumber(a)
	bf, bNum := toNumber(b)
	if aNum && bNum {
		switch op {
		case "<":
			return af < bf
		case "<=":
			return af <= bf
		case ">":
			return af > bf
		case ">=":
			return af >= bf
		}
	}
	as, bs := strings.ToLower(Stringify(a)), strings.ToLower(Stringify(b))
	switch op {
	case "<":
		return as < bs
	case "<=":
		return as <= bs
	case ">":
		return as > bs
	case ">=":
		return as >= bs
	}
	return false
}

func toNumber(v any) (float64, bool) {
	switch t := v.(type) {
	case float64:
		return t, true
	case int:
		return float64(t), true
	case int64:
		return float64(t), true
	case bool:
		if t {
			return 1, true
		}
		return 0, true
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(t), 64)
		if err != nil {
			return 0, false
		}
		return f, true
	}
	return 0, false
}

func call(name string, args []any, ctx map[string]any) (any, error) {
	switch strings.ToLower(name) {
	case "contains":
		if len(args) != 2 {
			return nil, fmt.Errorf("contains expects 2 arguments")
		}
		switch search := args[0].(type) {
		case []any:
			for _, item := range search {
				if equalValues(item, args[1]) {
					return true, nil
				}
			}
			return false, nil
		default:
			return strings.Contains(strings.ToLower(Stringify(args[0])), strings.ToLower(Stringify(args[1]))), nil
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
		items, ok := args[0].([]any)
		if !ok {
			return Stringify(args[0]), nil
		}
		parts := make([]string, len(items))
		for i, item := range items {
			parts[i] = Stringify(item)
		}
		return strings.Join(parts, sep), nil
	case "tojson":
		if len(args) != 1 {
			return nil, fmt.Errorf("toJSON expects 1 argument")
		}
		b, err := json.Marshal(args[0])
		if err != nil {
			return nil, err
		}
		return string(b), nil
	case "fromjson":
		if len(args) != 1 {
			return nil, fmt.Errorf("fromJSON expects 1 argument")
		}
		var v any
		if err := json.Unmarshal([]byte(Stringify(args[0])), &v); err != nil {
			return nil, fmt.Errorf("fromJSON: %w", err)
		}
		return normalizeJSON(v), nil
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
	case "hashfiles":
		return nil, fmt.Errorf("hashFiles is not supported by KERN yet")
	}
	return nil, fmt.Errorf("unknown function %q", name)
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
