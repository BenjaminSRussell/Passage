package domain

import (
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"
)

type HitPolicy string
type RuleLayer string

const (
	HitFirst      HitPolicy = "first"
	HitCollect    HitPolicy = "collect"
	LayerSystem   RuleLayer = "system"
	LayerCustomer RuleLayer = "customer"
)

type RuleColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

type DecisionRow struct {
	ID            string            `json:"id"`
	Layer         RuleLayer         `json:"layer,omitempty"`
	When          map[string]string `json:"when"`
	Then          map[string]any    `json:"then"`
	EffectiveFrom *time.Time        `json:"effectiveFrom,omitempty"`
	EffectiveTo   *time.Time        `json:"effectiveTo,omitempty"`
}

type DecisionTable struct {
	Name      string        `json:"name"`
	HitPolicy HitPolicy     `json:"hitPolicy"`
	Inputs    []RuleColumn  `json:"inputs"`
	Outputs   []RuleColumn  `json:"outputs"`
	Rows      []DecisionRow `json:"rows"`
}

type RuleMatch struct {
	RowID       string    `json:"rowId"`
	Layer       RuleLayer `json:"layer"`
	Explanation string    `json:"explanation"`
}

type DecisionResult struct {
	Outputs map[string]any `json:"outputs"`
	Matches []RuleMatch    `json:"matches"`
}

// Evaluate runs an effective-dated decision table and returns an explanation.
func Evaluate(table DecisionTable, input map[string]any, at time.Time) (DecisionResult, error) {
	if table.HitPolicy != HitFirst && table.HitPolicy != HitCollect {
		return DecisionResult{}, Violation{Path: "hitPolicy", Code: "MET-0301", Msg: "hit policy must be first or collect"}
	}
	columns := make(map[string]RuleColumn, len(table.Inputs))
	for _, column := range table.Inputs {
		columns[column.Name] = column
		if _, ok := input[column.Name]; !ok {
			return DecisionResult{}, Violation{Path: "input." + column.Name, Code: "MET-0301", Msg: "missing decision input"}
		}
		if err := validateRuleValue(column.Type, input[column.Name]); err != nil {
			return DecisionResult{}, Violation{Path: "input." + column.Name, Code: "MET-0301", Msg: err.Error()}
		}
	}
	rows := append([]DecisionRow(nil), table.Rows...)
	sort.SliceStable(rows, func(i, j int) bool {
		return rows[i].Layer == LayerCustomer && rows[j].Layer != LayerCustomer
	})
	result := DecisionResult{Outputs: map[string]any{}}
	for _, row := range rows {
		if (row.EffectiveFrom != nil && at.Before(*row.EffectiveFrom)) || (row.EffectiveTo != nil && !at.Before(*row.EffectiveTo)) {
			continue
		}
		ok := true
		var explanation []string
		keys := make([]string, 0, len(row.When))
		for key := range row.When {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			column, exists := columns[key]
			if !exists {
				return DecisionResult{}, Violation{Path: "rows." + row.ID + ".when." + key, Code: "MET-0301", Msg: "unknown input column"}
			}
			matched, err := matchRule(column.Type, input[key], row.When[key])
			if err != nil {
				return DecisionResult{}, Violation{Path: "rows." + row.ID + ".when." + key, Code: "MET-0301", Msg: err.Error()}
			}
			if !matched {
				ok = false
				break
			}
			explanation = append(explanation, key+" "+row.When[key])
		}
		if !ok {
			continue
		}
		for key, value := range row.Then {
			result.Outputs[key] = value
		}
		layer := row.Layer
		if layer == "" {
			layer = LayerSystem
		}
		result.Matches = append(result.Matches, RuleMatch{RowID: row.ID, Layer: layer, Explanation: strings.Join(explanation, " and ")})
		if table.HitPolicy == HitFirst {
			break
		}
	}
	if len(result.Matches) == 0 {
		return DecisionResult{}, Violation{Path: "rows", Code: "MET-0304", Msg: "no effective decision row matched"}
	}
	return result, nil
}

func validateRuleValue(kind string, value any) error {
	switch kind {
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("must be a string")
		}
	case "bool":
		switch value.(type) {
		case bool:
		case string:
			if _, err := strconv.ParseBool(value.(string)); err != nil {
				return fmt.Errorf("must be a boolean")
			}
		default:
			return fmt.Errorf("must be a boolean")
		}
	case "int32", "int64", "decimal":
		if _, ok := number(value); !ok {
			return fmt.Errorf("must be a number")
		}
	case "date", "datetime":
		text, ok := value.(string)
		if !ok {
			return fmt.Errorf("must be an RFC 3339 date")
		}
		layout := time.RFC3339
		if kind == "date" {
			layout = time.DateOnly
		}
		if _, err := time.Parse(layout, text); err != nil {
			return fmt.Errorf("must be an RFC 3339 date")
		}
	default:
		return fmt.Errorf("unsupported rule type %q", kind)
	}
	return nil
}

func matchRule(kind string, value any, condition string) (bool, error) {
	operators := []string{">=", "<=", "!=", "=", ">", "<"}
	op := ""
	for _, candidate := range operators {
		if strings.HasPrefix(condition, candidate) {
			op = candidate
			condition = strings.TrimSpace(strings.TrimPrefix(condition, candidate))
			break
		}
	}
	if op == "" {
		return false, fmt.Errorf("condition needs a comparison operator")
	}
	if kind == "int32" || kind == "int64" || kind == "decimal" {
		left, ok := number(value)
		if !ok {
			return false, fmt.Errorf("input must be numeric")
		}
		right := new(big.Rat)
		if _, ok := right.SetString(condition); !ok {
			return false, fmt.Errorf("condition must contain a number")
		}
		return compare(left.Cmp(right), op), nil
	}
	left := fmt.Sprint(value)
	return compare(strings.Compare(left, condition), op), nil
}

func number(value any) (*big.Rat, bool) {
	text := fmt.Sprint(value)
	rat := new(big.Rat)
	_, ok := rat.SetString(text)
	return rat, ok
}

func compare(order int, operator string) bool {
	switch operator {
	case "=":
		return order == 0
	case "!=":
		return order != 0
	case ">":
		return order > 0
	case "<":
		return order < 0
	case ">=":
		return order >= 0
	case "<=":
		return order <= 0
	}
	return false
}
