package bot

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"bot/internal/model"
)

const calculatorExpressionMax = 200

func calculatorTools(enabled bool) []model.ToolDefinition {
	if !enabled {
		return nil
	}
	return []model.ToolDefinition{{
		Name: "calculator", Description: "Evaluate a basic arithmetic expression with +, -, *, /, %, parentheses and numbers.",
		Parameters: map[string]any{
			"type":       "object",
			"properties": map[string]any{"expression": map[string]any{"type": "string", "description": "Arithmetic expression, for example 2+2*3"}},
			"required":   []string{"expression"},
		},
	}}
}

func executeCalculator(call model.ToolCall) (string, error) {
	if call.Name != "calculator" {
		return "", errors.New("unknown tool")
	}
	decoder := json.NewDecoder(strings.NewReader(call.Arguments))
	decoder.DisallowUnknownFields()
	var input struct {
		Expression string `json:"expression"`
	}
	if err := decoder.Decode(&input); err != nil {
		return "", err
	}
	if len(input.Expression) == 0 || len(input.Expression) > calculatorExpressionMax {
		return "", errors.New("calculator expression is empty or too long")
	}
	value, err := evaluateCalculator(input.Expression)
	if err != nil {
		return "", err
	}
	payload, err := json.Marshal(map[string]string{"result": strconv.FormatFloat(value, 'g', -1, 64)})
	return string(payload), err
}

type calculatorParser struct {
	input string
	pos   int
}

func evaluateCalculator(input string) (float64, error) {
	parser := &calculatorParser{input: strings.TrimSpace(input)}
	value, err := parser.expression()
	if err != nil {
		return 0, err
	}
	parser.space()
	if parser.pos != len(parser.input) {
		return 0, errors.New("unexpected characters in expression")
	}
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, errors.New("non-finite result")
	}
	return value, nil
}

func (p *calculatorParser) expression() (float64, error) {
	left, err := p.term()
	if err != nil {
		return 0, err
	}
	for {
		p.space()
		if p.pos >= len(p.input) || p.input[p.pos] != '+' && p.input[p.pos] != '-' {
			return left, nil
		}
		op := p.input[p.pos]
		p.pos++
		right, err := p.term()
		if err != nil {
			return 0, err
		}
		if op == '+' {
			left += right
		} else {
			left -= right
		}
	}
}

func (p *calculatorParser) term() (float64, error) {
	left, err := p.factor()
	if err != nil {
		return 0, err
	}
	for {
		p.space()
		if p.pos >= len(p.input) || !strings.ContainsRune("*/%", rune(p.input[p.pos])) {
			return left, nil
		}
		op := p.input[p.pos]
		p.pos++
		right, err := p.factor()
		if err != nil {
			return 0, err
		}
		switch op {
		case '*':
			left *= right
		case '/':
			if right == 0 {
				return 0, errors.New("division by zero")
			}
			left /= right
		case '%':
			if right == 0 {
				return 0, errors.New("modulo by zero")
			}
			left = math.Mod(left, right)
		}
	}
}

func (p *calculatorParser) factor() (float64, error) {
	p.space()
	if p.pos >= len(p.input) {
		return 0, errors.New("expression ended unexpectedly")
	}
	if p.input[p.pos] == '+' || p.input[p.pos] == '-' {
		sign := 1.0
		if p.input[p.pos] == '-' {
			sign = -1
		}
		p.pos++
		value, err := p.factor()
		return sign * value, err
	}
	if p.input[p.pos] == '(' {
		p.pos++
		value, err := p.expression()
		if err != nil {
			return 0, err
		}
		p.space()
		if p.pos >= len(p.input) || p.input[p.pos] != ')' {
			return 0, errors.New("missing closing parenthesis")
		}
		p.pos++
		return value, nil
	}
	start := p.pos
	for p.pos < len(p.input) && (p.input[p.pos] >= '0' && p.input[p.pos] <= '9' || p.input[p.pos] == '.') {
		p.pos++
	}
	if start == p.pos {
		return 0, fmt.Errorf("expected number at position %d", p.pos)
	}
	return strconv.ParseFloat(p.input[start:p.pos], 64)
}

func (p *calculatorParser) space() {
	for p.pos < len(p.input) && (p.input[p.pos] == ' ' || p.input[p.pos] == '\t' || p.input[p.pos] == '\n' || p.input[p.pos] == '\r') {
		p.pos++
	}
}
