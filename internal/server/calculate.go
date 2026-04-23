package server

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
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
	if err := decodeJSONBody(w, r, &req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	result := evaluate(req.Expr, req.Deg)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(calcResponse{Result: result})
}

// ── Token types ──

type tokenKind int

const (
	tokNum tokenKind = iota
	tokOp
	tokFunc
	tokLParen
	tokRParen
	tokPostfix
)

type token struct {
	kind tokenKind
	val  string
	num  float64
}

// ── Operator table ──

type opInfo struct {
	prec       int
	rightAssoc bool
}

var ops = map[string]opInfo{
	"+": {1, false},
	"-": {1, false},
	"*": {2, false},
	"/": {2, false},
	"^": {3, true},
}

// ── Tokenizer ──

var funcNames = []string{"asin", "acos", "atan", "sqrt", "sin", "cos", "tan", "lg", "ln"}
var reNumber = regexp.MustCompile(`^[0-9]*\.?[0-9]+([eE][+-]?[0-9]+)?`)
var reSignedNum = regexp.MustCompile(`^[+-][0-9]*\.?[0-9]+([eE][+-]?[0-9]+)?`)

func tokenize(expr string) ([]token, error) {
	tokens := make([]token, 0, 64)
	i := 0
	for i < len(expr) {
		ch := expr[i]

		if ch == ' ' || ch == '\t' {
			i++
			continue
		}

		// function names
		matched := false
		for _, fn := range funcNames {
			if i+len(fn) <= len(expr) && expr[i:i+len(fn)] == fn {
				if i+len(fn) < len(expr) && expr[i+len(fn)] == '(' {
					tokens = append(tokens, token{kind: tokFunc, val: fn})
					i += len(fn)
					matched = true
					break
				}
			}
		}
		if matched {
			continue
		}

		// 10^ prefix
		if i+3 <= len(expr) && expr[i:i+3] == "10^" {
			tokens = append(tokens, token{kind: tokFunc, val: "10^"})
			i += 3
			if i < len(expr) && expr[i] == '(' {
				tokens = append(tokens, token{kind: tokLParen, val: "("})
				i++
			}
			continue
		}

		// e^ prefix
		if i+2 <= len(expr) && expr[i:i+2] == "e^" {
			tokens = append(tokens, token{kind: tokFunc, val: "e^"})
			i += 2
			if i < len(expr) && expr[i] == '(' {
				tokens = append(tokens, token{kind: tokLParen, val: "("})
				i++
			}
			continue
		}

		// number literal
		if loc := reNumber.FindStringIndex(expr[i:]); loc != nil && loc[0] == 0 {
			numStr := expr[i : i+loc[1]]
			n, err := strconv.ParseFloat(numStr, 64)
			if err != nil {
				return nil, fmt.Errorf("bad number: %s", numStr)
			}
			tokens = append(tokens, token{kind: tokNum, num: n, val: numStr})
			i += loc[1]
			continue
		}

		// unary +/-
		if (ch == '-' || ch == '+') && (len(tokens) == 0 || tokens[len(tokens)-1].kind == tokOp || tokens[len(tokens)-1].kind == tokLParen || tokens[len(tokens)-1].kind == tokFunc) {
			if loc := reSignedNum.FindStringIndex(expr[i:]); loc != nil && loc[0] == 0 {
				numStr := expr[i : i+loc[1]]
				n, err := strconv.ParseFloat(numStr, 64)
				if err != nil {
					return nil, fmt.Errorf("bad number: %s", numStr)
				}
				tokens = append(tokens, token{kind: tokNum, num: n, val: numStr})
				i += loc[1]
				continue
			}
		}

		switch ch {
		case '+', '-', '*', '/', '^':
			tokens = append(tokens, token{kind: tokOp, val: string(ch)})
			i++
		case '(':
			tokens = append(tokens, token{kind: tokLParen, val: "("})
			i++
		case ')':
			tokens = append(tokens, token{kind: tokRParen, val: ")"})
			i++
		case '!':
			tokens = append(tokens, token{kind: tokPostfix, val: "!"})
			i++
		default:
			return nil, fmt.Errorf("unexpected char: %c", ch)
		}
	}
	return tokens, nil
}

// ── Shunting-yard algorithm ──
//
// Single-pass O(n) conversion from infix tokens to evaluated result.
// Uses a value stack (output) and an operator stack, applying operators
// immediately when popped — no intermediate RPN list needed.

func shuntingYard(tokens []token, deg bool) (float64, error) {
	output := make([]float64, 0, 32)
	opStack := make([]token, 0, 16)

	push := func(v float64) { output = append(output, v) }
	pop := func() (float64, error) {
		if len(output) == 0 {
			return 0, fmt.Errorf("stack underflow")
		}
		v := output[len(output)-1]
		output = output[:len(output)-1]
		return v, nil
	}

	applyBinOp := func(op string) error {
		b, err := pop()
		if err != nil {
			return err
		}
		a, err := pop()
		if err != nil {
			return err
		}
		switch op {
		case "+":
			push(a + b)
		case "-":
			push(a - b)
		case "*":
			push(a * b)
		case "/":
			push(a / b)
		case "^":
			push(math.Pow(a, b))
		}
		return nil
	}

	applyFunc := func(fn string) error {
		a, err := pop()
		if err != nil {
			return err
		}
		toRad := func(x float64) float64 {
			if deg {
				return x * math.Pi / 180
			}
			return x
		}
		fromRad := func(x float64) float64 {
			if deg {
				return x * 180 / math.Pi
			}
			return x
		}
		var v float64
		switch fn {
		case "sin":
			v = math.Sin(toRad(a))
		case "cos":
			v = math.Cos(toRad(a))
		case "tan":
			v = math.Tan(toRad(a))
		case "asin":
			v = fromRad(math.Asin(a))
		case "acos":
			v = fromRad(math.Acos(a))
		case "atan":
			v = fromRad(math.Atan(a))
		case "sqrt":
			v = math.Sqrt(a)
		case "lg":
			v = math.Log10(a)
		case "ln":
			v = math.Log(a)
		case "10^":
			v = math.Pow(10, a)
		case "e^":
			v = math.Exp(a)
		default:
			return fmt.Errorf("unknown func: %s", fn)
		}
		push(v)
		return nil
	}

	drainTop := func() error {
		top := opStack[len(opStack)-1]
		opStack = opStack[:len(opStack)-1]
		if top.kind == tokFunc {
			return applyFunc(top.val)
		}
		return applyBinOp(top.val)
	}

	for _, tok := range tokens {
		switch tok.kind {
		case tokNum:
			push(tok.num)

		case tokFunc:
			opStack = append(opStack, tok)

		case tokLParen:
			opStack = append(opStack, tok)

		case tokRParen:
			for len(opStack) > 0 && opStack[len(opStack)-1].kind != tokLParen {
				if err := drainTop(); err != nil {
					return 0, err
				}
			}
			if len(opStack) > 0 && opStack[len(opStack)-1].kind == tokLParen {
				opStack = opStack[:len(opStack)-1]
			}
			if len(opStack) > 0 && opStack[len(opStack)-1].kind == tokFunc {
				if err := drainTop(); err != nil {
					return 0, err
				}
			}

		case tokPostfix:
			a, err := pop()
			if err != nil {
				return 0, err
			}
			push(factorial(a))

		case tokOp:
			info := ops[tok.val]
			for len(opStack) > 0 {
				top := opStack[len(opStack)-1]
				if top.kind == tokLParen {
					break
				}
				if top.kind == tokFunc {
					if err := drainTop(); err != nil {
						return 0, err
					}
					continue
				}
				topInfo := ops[top.val]
				if topInfo.prec > info.prec || (topInfo.prec == info.prec && !info.rightAssoc) {
					if err := drainTop(); err != nil {
						return 0, err
					}
				} else {
					break
				}
			}
			opStack = append(opStack, tok)
		}
	}

	for len(opStack) > 0 {
		top := opStack[len(opStack)-1]
		if top.kind == tokLParen {
			opStack = opStack[:len(opStack)-1]
			continue
		}
		if err := drainTop(); err != nil {
			return 0, err
		}
	}

	if len(output) != 1 {
		return 0, fmt.Errorf("invalid expression")
	}
	return output[0], nil
}

// ── Public entry ──

func evaluate(raw string, deg bool) string {
	defer func() {
		if r := recover(); r != nil {}
	}()

	s := raw
	s = strings.ReplaceAll(s, "×", "*")
	s = strings.ReplaceAll(s, "÷", "/")
	s = strings.ReplaceAll(s, "−", "-")
	s = strings.ReplaceAll(s, "π", fmt.Sprintf("(%v)", math.Pi))
	s = strings.ReplaceAll(s, "%", "/100")

	// standalone 'e' constant
	out := strings.Builder{}
	for i := 0; i < len(s); i++ {
		if s[i] == 'e' {
			before := i > 0 && (isAlpha(s[i-1]) || isDigit(s[i-1]) || s[i-1] == '.')
			after := i+1 < len(s) && (isAlpha(s[i+1]) || s[i+1] == '^')
			if !before && !after {
				out.WriteString(fmt.Sprintf("(%v)", math.E))
				continue
			}
		}
		out.WriteByte(s[i])
	}

	tokens, err := tokenize(out.String())
	if err != nil {
		return "Error"
	}

	val, err := shuntingYard(tokens, deg)
	if err != nil {
		return "Error"
	}

	if math.IsInf(val, 1) {
		return "∞"
	}
	if math.IsInf(val, -1) {
		return "-∞"
	}
	if math.IsNaN(val) {
		return "Error"
	}
	return fmt.Sprintf("%.12g", val)
}

func isAlpha(b byte) bool { return unicode.IsLetter(rune(b)) }
func isDigit(b byte) bool { return b >= '0' && b <= '9' }

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
