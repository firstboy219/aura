package tools

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"unicode"
)

// calc evaluates arithmetic exactly with rational numbers (Blueprint P2: numbers
// in results are produced or recomputed with calc). Supported: + - * / ^
// (integer exponents), parentheses, unary minus, and functions round(x, n),
// abs(x), min(a, b, ...), max(a, b, ...), sqrt(x) (approximate, flagged).

var ErrCalc = errors.New("calc_error")

const (
	maxExprLen   = 500
	maxCalcDepth = 50
	maxExponent  = 1000
	maxRatBits   = 1 << 16
)

// CalcResult is the result of an expression.
type CalcResult struct {
	Expression string `json:"expression"`
	Result     string `json:"result"`   // decimal, at most 20 places, trailing zeros trimmed
	Fraction   string `json:"fraction"` // exact p/q
	Exact      bool   `json:"exact"`    // false if the decimal was rounded or sqrt was used
}

type calcParser struct {
	s       string
	pos     int
	depth   int
	inexact bool
}

// Calc evaluates an expression.
func Calc(expr string) (*CalcResult, error) {
	if strings.TrimSpace(expr) == "" || len(expr) > maxExprLen {
		return nil, fmt.Errorf("%w: expression must be 1..%d characters", ErrCalc, maxExprLen)
	}
	p := &calcParser{s: expr}
	v, err := p.expr()
	if err != nil {
		return nil, err
	}
	p.skip()
	if p.pos != len(p.s) {
		return nil, fmt.Errorf("%w: unexpected %q at %d", ErrCalc, p.s[p.pos:], p.pos)
	}
	dec, exactDec := FormatRat(v, 20)
	return &CalcResult{Expression: expr, Result: dec, Fraction: v.RatString(), Exact: exactDec && !p.inexact}, nil
}

// FormatRat formats r with at most places decimals (trailing zeros trimmed)
// and reports whether that decimal is exact.
func FormatRat(r *big.Rat, places int) (string, bool) {
	s := r.FloatString(places)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	back, ok := new(big.Rat).SetString(s)
	return s, ok && back.Cmp(r) == 0
}

func (p *calcParser) skip() {
	for p.pos < len(p.s) && unicode.IsSpace(rune(p.s[p.pos])) {
		p.pos++
	}
}

func (p *calcParser) peek() byte {
	p.skip()
	if p.pos < len(p.s) {
		return p.s[p.pos]
	}
	return 0
}

func (p *calcParser) enter() error {
	p.depth++
	if p.depth > maxCalcDepth {
		return fmt.Errorf("%w: expression nested too deeply", ErrCalc)
	}
	return nil
}

func checkSize(r *big.Rat) error {
	if r.Num().BitLen() > maxRatBits || r.Denom().BitLen() > maxRatBits {
		return fmt.Errorf("%w: number too large", ErrCalc)
	}
	return nil
}

func (p *calcParser) expr() (*big.Rat, error) {
	if err := p.enter(); err != nil {
		return nil, err
	}
	defer func() { p.depth-- }()
	v, err := p.term()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '+', '-':
			op := p.s[p.pos]
			p.pos++
			r, err := p.term()
			if err != nil {
				return nil, err
			}
			if op == '+' {
				v = new(big.Rat).Add(v, r)
			} else {
				v = new(big.Rat).Sub(v, r)
			}
		default:
			return v, checkSize(v)
		}
	}
}

func (p *calcParser) term() (*big.Rat, error) {
	v, err := p.power()
	if err != nil {
		return nil, err
	}
	for {
		switch p.peek() {
		case '*', '/':
			op := p.s[p.pos]
			p.pos++
			r, err := p.power()
			if err != nil {
				return nil, err
			}
			if op == '*' {
				v = new(big.Rat).Mul(v, r)
			} else {
				if r.Sign() == 0 {
					return nil, fmt.Errorf("%w: division by zero", ErrCalc)
				}
				v = new(big.Rat).Quo(v, r)
			}
			if err := checkSize(v); err != nil {
				return nil, err
			}
		default:
			return v, nil
		}
	}
}

func (p *calcParser) power() (*big.Rat, error) {
	base, err := p.unary()
	if err != nil {
		return nil, err
	}
	if p.peek() != '^' {
		return base, nil
	}
	p.pos++
	exp, err := p.power() // right-associative
	if err != nil {
		return nil, err
	}
	if !exp.IsInt() || exp.Num().BitLen() > 16 {
		return nil, fmt.Errorf("%w: exponent must be an integer up to %d", ErrCalc, maxExponent)
	}
	n := exp.Num().Int64()
	if n > maxExponent || n < -maxExponent {
		return nil, fmt.Errorf("%w: exponent must be an integer up to %d", ErrCalc, maxExponent)
	}
	if n < 0 && base.Sign() == 0 {
		return nil, fmt.Errorf("%w: division by zero", ErrCalc)
	}
	neg := n < 0
	if neg {
		n = -n
	}
	num := new(big.Int).Exp(base.Num(), big.NewInt(n), nil)
	den := new(big.Int).Exp(base.Denom(), big.NewInt(n), nil)
	r := new(big.Rat).SetFrac(num, den)
	if neg {
		r.Inv(r)
	}
	return r, checkSize(r)
}

func (p *calcParser) unary() (*big.Rat, error) {
	switch p.peek() {
	case '-':
		p.pos++
		v, err := p.unary()
		if err != nil {
			return nil, err
		}
		return new(big.Rat).Neg(v), nil
	case '+':
		p.pos++
		return p.unary()
	}
	return p.primary()
}

func (p *calcParser) primary() (*big.Rat, error) {
	c := p.peek()
	switch {
	case c == '(':
		p.pos++
		v, err := p.expr()
		if err != nil {
			return nil, err
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("%w: missing )", ErrCalc)
		}
		p.pos++
		return v, nil
	case c >= '0' && c <= '9' || c == '.':
		start := p.pos
		for p.pos < len(p.s) && (p.s[p.pos] >= '0' && p.s[p.pos] <= '9' || p.s[p.pos] == '.' || p.s[p.pos] == '_') {
			p.pos++
		}
		if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
			q := p.pos + 1
			if q < len(p.s) && (p.s[q] == '+' || p.s[q] == '-') {
				q++
			}
			if q < len(p.s) && p.s[q] >= '0' && p.s[q] <= '9' {
				p.pos = q
				for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
					p.pos++
				}
			}
		}
		lit := strings.ReplaceAll(p.s[start:p.pos], "_", "")
		r, ok := new(big.Rat).SetString(lit)
		if !ok {
			return nil, fmt.Errorf("%w: bad number %q", ErrCalc, lit)
		}
		return r, checkSize(r)
	case unicode.IsLetter(rune(c)):
		start := p.pos
		for p.pos < len(p.s) && unicode.IsLetter(rune(p.s[p.pos])) {
			p.pos++
		}
		name := strings.ToLower(p.s[start:p.pos])
		if p.peek() != '(' {
			return nil, fmt.Errorf("%w: unknown name %q", ErrCalc, name)
		}
		p.pos++
		var args []*big.Rat
		for {
			v, err := p.expr()
			if err != nil {
				return nil, err
			}
			args = append(args, v)
			if p.peek() == ',' {
				p.pos++
				continue
			}
			break
		}
		if p.peek() != ')' {
			return nil, fmt.Errorf("%w: missing ) after %s", ErrCalc, name)
		}
		p.pos++
		return p.call(name, args)
	}
	return nil, fmt.Errorf("%w: unexpected input at %d", ErrCalc, p.pos)
}

func (p *calcParser) call(name string, args []*big.Rat) (*big.Rat, error) {
	need := func(n int) error {
		if len(args) != n {
			return fmt.Errorf("%w: %s takes %d argument(s)", ErrCalc, name, n)
		}
		return nil
	}
	switch name {
	case "abs":
		if err := need(1); err != nil {
			return nil, err
		}
		return new(big.Rat).Abs(args[0]), nil
	case "min", "max":
		best := args[0]
		for _, a := range args[1:] {
			if (name == "min" && a.Cmp(best) < 0) || (name == "max" && a.Cmp(best) > 0) {
				best = a
			}
		}
		return best, nil
	case "round":
		if err := need(2); err != nil {
			return nil, err
		}
		if !args[1].IsInt() || args[1].Sign() < 0 || args[1].Num().Int64() > 30 {
			return nil, fmt.Errorf("%w: round places must be an integer 0..30", ErrCalc)
		}
		s := args[0].FloatString(int(args[1].Num().Int64())) // rounds half away from zero
		r, _ := new(big.Rat).SetString(s)
		return r, nil
	case "sqrt":
		if err := need(1); err != nil {
			return nil, err
		}
		if args[0].Sign() < 0 {
			return nil, fmt.Errorf("%w: sqrt of a negative number", ErrCalc)
		}
		f := new(big.Float).SetPrec(200).SetRat(args[0])
		f.Sqrt(f)
		r, _ := f.Rat(nil)
		if new(big.Rat).Mul(r, r).Cmp(args[0]) != 0 {
			p.inexact = true
		}
		return r, nil
	}
	return nil, fmt.Errorf("%w: unknown function %q", ErrCalc, name)
}
