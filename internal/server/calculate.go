package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strings"
	"unicode"
)

type calcRequest struct {
	Expr string `json:"expr"`
	Deg  bool   `json:"deg"`
}

type calcResponse struct {
	Result string `json:"result"`
}

func (s *Server) handleCalculate(w http.ResponseWriter, r *http.Request) {
	var req calcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	result := evaluate(req.Expr, req.Deg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(calcResponse{Result: result})
}

// ── recursive descent expression parser ──

type parser struct {
	src string
	pos int
	deg bool
}

func evaluate(raw string, deg bool) string {
	defer func() {
		if r := recover(); r != nil {
			// swallow parse panics
		}
	}()

	// normalise symbols
	s := raw
	s = strings.ReplaceAll(s, "×", "*")
	s = strings.ReplaceAll(s, "÷", "/")
	s = strings.ReplaceAll(s, "−", "-")
	s = strings.ReplaceAll(s, "π", fmt.Sprintf("(%v)", math.Pi))
	s = strings.ReplaceAll(s, "%", "/100")

	// standalone 'e' constant (not part of e^ or scientific notation)
	out := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] == 'e' {
			before := i > 0 && (isAlpha(s[i-1]))
			after := i+1 < len(s) && (isAlpha(s[i+1]) || s[i+1] == '^')
			if !before && !after {
				out.WriteString(fmt.Sprintf("(%v)", math.E))
				continue
			}
		}
		out.WriteByte(s[i])
	}

	p := &parser{src: out.String(), pos: 0, deg: deg}
	val := p.parseExpr()
	if math.IsInf(val, 1) {
		return "∞"
	}
	if math.IsInf(val, -1) {
		return "-∞"
	}
	if math.IsNaN(val) {
		return "Error"
	}
	// format: up to 12 significant digits, strip trailing zeros
	txt := fmt.Sprintf("%.12g", val)
	return txt
}

func isAlpha(b byte) bool { return unicode.IsLetter(rune(b)) }

func (p *parser) ws() {
	for p.pos < len(p.src) && p.src[p.pos] == ' ' {
		p.pos++
	}
}

func (p *parser) tryMatch(s string) bool {
	p.ws()
	if p.pos+len(s) <= len(p.src) && p.src[p.pos:p.pos+len(s)] == s {
		p.pos += len(s)
		return true
	}
	return false
}

func (p *parser) peek() byte {
	p.ws()
	if p.pos >= len(p.src) {
		return 0
	}
	return p.src[p.pos]
}

func (p *parser) parseExpr() float64 {
	v := p.parseMulDiv()
	for {
		p.ws()
		if p.pos >= len(p.src) {
			break
		}
		if p.src[p.pos] == '+' {
			p.pos++
			v += p.parseMulDiv()
		} else if p.src[p.pos] == '-' {
			p.pos++
			v -= p.parseMulDiv()
		} else {
			break
		}
	}
	return v
}

func (p *parser) parseMulDiv() float64 {
	v := p.parsePow()
	for {
		p.ws()
		if p.pos >= len(p.src) {
			break
		}
		if p.src[p.pos] == '*' && (p.pos+1 >= len(p.src) || p.src[p.pos+1] != '*') {
			p.pos++
			v *= p.parsePow()
		} else if p.src[p.pos] == '/' {
			p.pos++
			v /= p.parsePow()
		} else {
			break
		}
	}
	return v
}

func (p *parser) parsePow() float64 {
	b := p.parseUnary()
	p.ws()
	if p.pos < len(p.src) && p.src[p.pos] == '^' {
		p.pos++
		b = math.Pow(b, p.parsePow()) // right-associative
	} else if p.pos+1 < len(p.src) && p.src[p.pos] == '*' && p.src[p.pos+1] == '*' {
		p.pos += 2
		b = math.Pow(b, p.parsePow())
	}
	return b
}

func (p *parser) parseUnary() float64 {
	p.ws()
	if p.pos < len(p.src) && p.src[p.pos] == '-' {
		p.pos++
		return -p.parsePostfix()
	}
	if p.pos < len(p.src) && p.src[p.pos] == '+' {
		p.pos++
	}
	return p.parsePostfix()
}

func (p *parser) parsePostfix() float64 {
	v := p.parseAtom()
	p.ws()
	for p.pos < len(p.src) && p.src[p.pos] == '!' {
		p.pos++
		v = factorial(v)
	}
	return v
}

func (p *parser) parseAtom() float64 {
	p.ws()

	// named functions
	fns := []string{"asin", "acos", "atan", "sqrt", "sin", "cos", "tan", "lg", "ln"}
	for _, fn := range fns {
		if p.tryMatch(fn + "(") {
			a := p.parseExpr()
			if p.pos < len(p.src) && p.src[p.pos] == ')' {
				p.pos++
			}
			return p.applyFunc(fn, a)
		}
	}

	// 10^
	if p.tryMatch("10^") {
		return math.Pow(10, p.parseUnary())
	}
	// e^
	if p.tryMatch("e^") {
		return math.Exp(p.parseUnary())
	}

	// parenthesised sub-expression
	if p.peek() == '(' {
		p.pos++
		v := p.parseExpr()
		if p.pos < len(p.src) && p.src[p.pos] == ')' {
			p.pos++
		}
		return v
	}

	// number literal
	start := p.pos
	if p.pos < len(p.src) && (p.src[p.pos] == '-' || p.src[p.pos] == '+') {
		p.pos++
	}
	for p.pos < len(p.src) && (p.src[p.pos] >= '0' && p.src[p.pos] <= '9' || p.src[p.pos] == '.') {
		p.pos++
	}
	// scientific notation e.g. 1.5e10
	if p.pos < len(p.src) && (p.src[p.pos] == 'e' || p.src[p.pos] == 'E') {
		p.pos++
		if p.pos < len(p.src) && (p.src[p.pos] == '+' || p.src[p.pos] == '-') {
			p.pos++
		}
		for p.pos < len(p.src) && p.src[p.pos] >= '0' && p.src[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos == start {
		panic("unexpected token")
	}
	var n float64
	_, err := fmt.Sscanf(p.src[start:p.pos], "%g", &n)
	if err != nil {
		panic("bad number")
	}
	return n
}

func (p *parser) applyFunc(fn string, a float64) float64 {
	toRad := func(x float64) float64 {
		if p.deg {
			return x * math.Pi / 180
		}
		return x
	}
	fromRad := func(x float64) float64 {
		if p.deg {
			return x * 180 / math.Pi
		}
		return x
	}
	switch fn {
	case "sin":
		return math.Sin(toRad(a))
	case "cos":
		return math.Cos(toRad(a))
	case "tan":
		return math.Tan(toRad(a))
	case "asin":
		return fromRad(math.Asin(a))
	case "acos":
		return fromRad(math.Acos(a))
	case "atan":
		return fromRad(math.Atan(a))
	case "sqrt":
		return math.Sqrt(a)
	case "lg":
		return math.Log10(a)
	case "ln":
		return math.Log(a)
	}
	return math.NaN()
}

func factorial(n float64) float64 {
	if n < 0 || n != math.Floor(n) || n > 170 {
		return math.NaN()
	}
	r := 1.0
	for i := 2.0; i <= n; i++ {
		r *= i
	}
	return r
}
