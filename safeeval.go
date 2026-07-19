package ear

// A restricted boolean/arithmetic expression evaluator, used as a Policy's
// deterministic fallback when no LLM provider is active. It never passes an
// expression to anything that could execute arbitrary code: it tokenizes
// and parses the expression into an AST of literals, names, comparisons,
// boolean logic and basic arithmetic, then walks it.
//
// Grammar (lowest to highest precedence):
//
//	or   := and ("or" and)*
//	and  := not ("and" not)*
//	not  := "not" not | comparison
//	comp := sum (("=="|"!="|"<"|"<="|">"|">="|"in"|"not in") sum)*
//	sum  := term (("+"|"-") term)*
//	term := unary (("*"|"/"|"//"|"%") unary)*
//	unary:= ("-"|"+") unary | power
//	power:= atom ("**" unary)?
//	atom := number | string | "true"|"false"|"none" | name
//	        | "(" or ")" | "[" (or ("," or)*)? "]"

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode"
)

// MissingVariableError reports an expression referencing a name absent from
// the given context; the policy is then treated as not applicable to the
// intent rather than failing.
type MissingVariableError struct{ Name string }

func (e *MissingVariableError) Error() string { return "unknown variable: " + e.Name }

// UnsafeExpressionError reports a construct outside the allowed grammar.
type UnsafeExpressionError struct{ Detail string }

func (e *UnsafeExpressionError) Error() string { return "unsafe expression: " + e.Detail }

// SafeEval evaluates expression against variables using only literals,
// names, comparisons, boolean operators and arithmetic.
func SafeEval(expression string, variables map[string]any) (any, error) {
	toks, err := tokenize(expression)
	if err != nil {
		return nil, err
	}
	p := &exprParser{toks: toks, vars: variables}
	value, err := p.parseOr()
	if err != nil {
		return nil, err
	}
	if p.peek().kind != tokEOF {
		return nil, &UnsafeExpressionError{Detail: "trailing tokens: " + p.peek().text}
	}
	return value, nil
}

type tokKind int

const (
	tokNumber tokKind = iota
	tokString
	tokName
	tokOp
	tokEOF
)

type token struct {
	kind tokKind
	text string
}

func tokenize(s string) ([]token, error) {
	var toks []token
	runes := []rune(s)
	i := 0
	twoChar := map[string]bool{"==": true, "!=": true, "<=": true, ">=": true, "//": true, "**": true}
	for i < len(runes) {
		c := runes[i]
		switch {
		case unicode.IsSpace(c):
			i++
		case c == '"' || c == '\'':
			quote := c
			i++
			var sb strings.Builder
			for i < len(runes) && runes[i] != quote {
				sb.WriteRune(runes[i])
				i++
			}
			if i >= len(runes) {
				return nil, &UnsafeExpressionError{Detail: "unterminated string"}
			}
			i++ // closing quote
			toks = append(toks, token{tokString, sb.String()})
		case unicode.IsDigit(c) || (c == '.' && i+1 < len(runes) && unicode.IsDigit(runes[i+1])):
			start := i
			for i < len(runes) && (unicode.IsDigit(runes[i]) || runes[i] == '.') {
				i++
			}
			toks = append(toks, token{tokNumber, string(runes[start:i])})
		case unicode.IsLetter(c) || c == '_':
			start := i
			for i < len(runes) && (unicode.IsLetter(runes[i]) || unicode.IsDigit(runes[i]) || runes[i] == '_') {
				i++
			}
			toks = append(toks, token{tokName, string(runes[start:i])})
		default:
			if i+1 < len(runes) && twoChar[string(runes[i:i+2])] {
				toks = append(toks, token{tokOp, string(runes[i : i+2])})
				i += 2
				continue
			}
			if !strings.ContainsRune("+-*/%<>()[],", c) {
				return nil, &UnsafeExpressionError{Detail: "unexpected character: " + string(c)}
			}
			toks = append(toks, token{tokOp, string(c)})
			i++
		}
	}
	toks = append(toks, token{tokEOF, ""})
	return toks, nil
}

type exprParser struct {
	toks []token
	pos  int
	vars map[string]any
}

func (p *exprParser) peek() token { return p.toks[p.pos] }

func (p *exprParser) acceptName(word string) bool {
	t := p.peek()
	if t.kind == tokName && t.text == word {
		p.pos++
		return true
	}
	return false
}

func (p *exprParser) acceptOp(op string) bool {
	t := p.peek()
	if t.kind == tokOp && t.text == op {
		p.pos++
		return true
	}
	return false
}

func (p *exprParser) parseOr() (any, error) {
	left, err := p.parseAnd()
	if err != nil {
		return nil, err
	}
	for p.acceptName("or") {
		right, err := p.parseAnd()
		if err != nil {
			return nil, err
		}
		left = truthy(left) || truthy(right)
	}
	return left, nil
}

func (p *exprParser) parseAnd() (any, error) {
	left, err := p.parseNot()
	if err != nil {
		return nil, err
	}
	for p.acceptName("and") {
		right, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		left = truthy(left) && truthy(right)
	}
	return left, nil
}

func (p *exprParser) parseNot() (any, error) {
	if p.acceptName("not") {
		operand, err := p.parseNot()
		if err != nil {
			return nil, err
		}
		return !truthy(operand), nil
	}
	return p.parseComparison()
}

func (p *exprParser) parseComparison() (any, error) {
	left, err := p.parseSum()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		switch {
		case p.acceptOp("=="):
			op = "=="
		case p.acceptOp("!="):
			op = "!="
		case p.acceptOp("<="):
			op = "<="
		case p.acceptOp(">="):
			op = ">="
		case p.acceptOp("<"):
			op = "<"
		case p.acceptOp(">"):
			op = ">"
		case p.acceptName("in"):
			op = "in"
		default:
			// "not in" -- two words.
			save := p.pos
			if p.acceptName("not") && p.acceptName("in") {
				op = "not in"
			} else {
				p.pos = save
			}
		}
		if op == "" {
			return left, nil
		}
		right, err := p.parseSum()
		if err != nil {
			return nil, err
		}
		result, err := compare(op, left, right)
		if err != nil {
			return nil, err
		}
		left = result
	}
}

func (p *exprParser) parseSum() (any, error) {
	left, err := p.parseTerm()
	if err != nil {
		return nil, err
	}
	for {
		switch {
		case p.acceptOp("+"):
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			if left, err = arith("+", left, right); err != nil {
				return nil, err
			}
		case p.acceptOp("-"):
			right, err := p.parseTerm()
			if err != nil {
				return nil, err
			}
			if left, err = arith("-", left, right); err != nil {
				return nil, err
			}
		default:
			return left, nil
		}
	}
}

func (p *exprParser) parseTerm() (any, error) {
	left, err := p.parseUnary()
	if err != nil {
		return nil, err
	}
	for {
		op := ""
		switch {
		case p.acceptOp("*"):
			op = "*"
		case p.acceptOp("//"):
			op = "//"
		case p.acceptOp("/"):
			op = "/"
		case p.acceptOp("%"):
			op = "%"
		default:
			return left, nil
		}
		right, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		if left, err = arith(op, left, right); err != nil {
			return nil, err
		}
	}
}

func (p *exprParser) parseUnary() (any, error) {
	if p.acceptOp("-") {
		operand, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		f, ok := toNumber(operand)
		if !ok {
			return nil, &UnsafeExpressionError{Detail: "unary minus on non-number"}
		}
		return -f, nil
	}
	if p.acceptOp("+") {
		return p.parseUnary()
	}
	return p.parsePower()
}

func (p *exprParser) parsePower() (any, error) {
	base, err := p.parseAtom()
	if err != nil {
		return nil, err
	}
	if p.acceptOp("**") {
		exp, err := p.parseUnary()
		if err != nil {
			return nil, err
		}
		return arith("**", base, exp)
	}
	return base, nil
}

func (p *exprParser) parseAtom() (any, error) {
	t := p.peek()
	switch t.kind {
	case tokNumber:
		p.pos++
		f, err := strconv.ParseFloat(t.text, 64)
		if err != nil {
			return nil, &UnsafeExpressionError{Detail: "bad number: " + t.text}
		}
		return f, nil
	case tokString:
		p.pos++
		return t.text, nil
	case tokName:
		switch t.text {
		case "true", "True":
			p.pos++
			return true, nil
		case "false", "False":
			p.pos++
			return false, nil
		case "none", "None", "null":
			p.pos++
			return nil, nil
		}
		p.pos++
		value, ok := p.vars[t.text]
		if !ok {
			return nil, &MissingVariableError{Name: t.text}
		}
		return value, nil
	case tokOp:
		if t.text == "(" {
			p.pos++
			value, err := p.parseOr()
			if err != nil {
				return nil, err
			}
			if !p.acceptOp(")") {
				return nil, &UnsafeExpressionError{Detail: "missing )"}
			}
			return value, nil
		}
		if t.text == "[" {
			p.pos++
			var list []any
			if !p.acceptOp("]") {
				for {
					value, err := p.parseOr()
					if err != nil {
						return nil, err
					}
					list = append(list, value)
					if p.acceptOp(",") {
						continue
					}
					if p.acceptOp("]") {
						break
					}
					return nil, &UnsafeExpressionError{Detail: "malformed list"}
				}
			}
			return list, nil
		}
	}
	return nil, &UnsafeExpressionError{Detail: "unexpected token: " + t.text}
}

func toNumber(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case bool:
		if n {
			return 1, true
		}
		return 0, true
	}
	return 0, false
}

func truthy(v any) bool {
	switch n := v.(type) {
	case bool:
		return n
	case float64:
		return n != 0
	case string:
		return n != ""
	case nil:
		return false
	case []any:
		return len(n) > 0
	}
	return true
}

func arith(op string, a, b any) (any, error) {
	x, ok1 := toNumber(a)
	y, ok2 := toNumber(b)
	if !ok1 || !ok2 {
		if op == "+" {
			if sa, ok := a.(string); ok {
				if sb, ok := b.(string); ok {
					return sa + sb, nil
				}
			}
		}
		return nil, &UnsafeExpressionError{Detail: "arithmetic on non-number"}
	}
	switch op {
	case "+":
		return x + y, nil
	case "-":
		return x - y, nil
	case "*":
		return x * y, nil
	case "/":
		if y == 0 {
			return nil, &UnsafeExpressionError{Detail: "division by zero"}
		}
		return x / y, nil
	case "//":
		if y == 0 {
			return nil, &UnsafeExpressionError{Detail: "division by zero"}
		}
		return math.Floor(x / y), nil
	case "%":
		if y == 0 {
			return nil, &UnsafeExpressionError{Detail: "division by zero"}
		}
		return math.Mod(x, y), nil
	case "**":
		return math.Pow(x, y), nil
	}
	return nil, &UnsafeExpressionError{Detail: "unsupported operator " + op}
}

func compare(op string, a, b any) (any, error) {
	switch op {
	case "==":
		return equal(a, b), nil
	case "!=":
		return !equal(a, b), nil
	case "in":
		return contains(b, a), nil
	case "not in":
		return !contains(b, a), nil
	}
	x, ok1 := toNumber(a)
	y, ok2 := toNumber(b)
	if ok1 && ok2 {
		switch op {
		case "<":
			return x < y, nil
		case "<=":
			return x <= y, nil
		case ">":
			return x > y, nil
		case ">=":
			return x >= y, nil
		}
	}
	sa, oka := a.(string)
	sb, okb := b.(string)
	if oka && okb {
		switch op {
		case "<":
			return sa < sb, nil
		case "<=":
			return sa <= sb, nil
		case ">":
			return sa > sb, nil
		case ">=":
			return sa >= sb, nil
		}
	}
	return nil, &UnsafeExpressionError{Detail: "cannot order these values"}
}

func equal(a, b any) bool {
	if x, ok := toNumber(a); ok {
		if y, ok := toNumber(b); ok {
			return x == y
		}
	}
	return fmt.Sprint(a) == fmt.Sprint(b)
}

func contains(container, item any) bool {
	switch c := container.(type) {
	case string:
		return strings.Contains(c, fmt.Sprint(item))
	case []any:
		for _, e := range c {
			if equal(e, item) {
				return true
			}
		}
	}
	return false
}
