package expressions

// Validate parses an expression body without evaluating it. Compile-time validation
// uses this so context values do not need to exist yet.
func Validate(body string) error {
	p, err := newParser(body)
	if err != nil {
		return err
	}
	if _, err := p.parseExpression(); err != nil {
		return err
	}
	if p.err != nil {
		return p.err
	}
	if p.peek().kind != tokEOF {
		return errUnexpected(p.peek().text)
	}
	return nil
}

func errUnexpected(text string) error {
	return &unexpectedTokenError{text: text}
}

type unexpectedTokenError struct{ text string }

func (e *unexpectedTokenError) Error() string {
	return "unexpected token " + e.text + " in expression"
}
