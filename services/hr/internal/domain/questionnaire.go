package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
)

const (
	QuestionSingle  = "SINGLE"
	QuestionMulti   = "MULTI"
	QuestionScale   = "SCALE"
	QuestionNumeric = "NUMERIC"
	QuestionText    = "TEXT"
	QuestionDate    = "DATE"

	CodeQuestionnaireClosed    = "HCM-QST-001"
	CodeQuestionnaireThreshold = "HCM-QST-002"
	CodeQuestionnaireInvalid   = "HCM-QST-INVALID"

	AnonymityThreshold = 5
	maxQuestionText    = 500
	maxAnswerText      = 2000
)

var (
	questionCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{0,19}$`)
	questionKind = []string{QuestionSingle, QuestionMulti, QuestionScale, QuestionNumeric, QuestionText, QuestionDate}
	scoreBound   = decimal.New(1, 9)
)

// Option is one entry of an answer group. Score is the number a SINGLE answer
// contributes to its result group.
type Option struct {
	Value string  `json:"value"`
	Label string  `json:"label"`
	Score *string `json:"score,omitempty"`
}

// Question is one question of a questionnaire. Weight is a decimal string.
// SCALE, NUMERIC and SINGLE (with scored options) feed their ResultGroup.
type Question struct {
	Code        string   `json:"code"`
	Text        string   `json:"text"`
	Type        string   `json:"type"`
	Options     []Option `json:"options,omitempty"`
	ScaleMax    int      `json:"scaleMax,omitempty"`
	Weight      string   `json:"weight"`
	Mandatory   bool     `json:"mandatory"`
	ResultGroup string   `json:"resultGroup,omitempty"`
}

func (q Question) scored() bool {
	if q.Type == QuestionSingle {
		for _, o := range q.Options {
			if o.Score != nil {
				return true
			}
		}
		return false
	}
	return q.Type == QuestionScale || q.Type == QuestionNumeric
}

// NormalizeQuestions validates a questionnaire design and fills the defaults
// (weight 1, scale 5). The returned map names each broken field.
func NormalizeQuestions(in []Question) ([]Question, map[string]string) {
	fields := map[string]string{}
	out := make([]Question, len(in))
	seen := map[string]bool{}
	for i, q := range in {
		at := func(f string) string { return fmt.Sprintf("questions[%d].%s", i, f) }
		if !questionCode.MatchString(q.Code) || seen[q.Code] {
			fields[at("code")] = "must be unique and match [A-Z0-9][A-Z0-9_-]{0,19}"
		}
		seen[q.Code] = true
		if n := utf8.RuneCountInString(q.Text); n < 1 || n > maxQuestionText {
			fields[at("text")] = "must be 1 to 500 characters"
		}
		if !inList(questionKind, q.Type) {
			fields[at("type")] = "must be one of SINGLE, MULTI, SCALE, NUMERIC, TEXT, DATE"
		}
		if q.ResultGroup != "" && !questionCode.MatchString(q.ResultGroup) {
			fields[at("resultGroup")] = "must match [A-Z0-9][A-Z0-9_-]{0,19}"
		}
		if q.Weight == "" {
			q.Weight = "1"
		}
		if w, err := decimal.NewFromString(q.Weight); err != nil || !w.IsPositive() || w.GreaterThan(decimal.NewFromInt(100)) {
			fields[at("weight")] = "must be a decimal above 0 and at most 100"
		}
		switch q.Type {
		case QuestionSingle, QuestionMulti:
			q.ScaleMax = 0
			if len(q.Options) < 2 || len(q.Options) > 20 {
				fields[at("options")] = "needs 2 to 20 options"
			}
			values := map[string]bool{}
			for j, o := range q.Options {
				if !questionCode.MatchString(o.Value) || values[o.Value] || o.Label == "" || utf8.RuneCountInString(o.Label) > 120 {
					fields[fmt.Sprintf("questions[%d].options[%d]", i, j)] = "value must be unique [A-Z0-9][A-Z0-9_-]{0,19} and label 1 to 120 characters"
				}
				values[o.Value] = true
				if o.Score != nil {
					if s, err := decimal.NewFromString(*o.Score); err != nil || s.Abs().GreaterThan(scoreBound) {
						fields[fmt.Sprintf("questions[%d].options[%d].score", i, j)] = "must be a decimal"
					}
				}
			}
		case QuestionScale:
			q.Options = nil
			if q.ScaleMax == 0 {
				q.ScaleMax = 5
			}
			if q.ScaleMax < 2 || q.ScaleMax > 10 {
				fields[at("scaleMax")] = "must be 2 to 10"
			}
		default:
			q.Options, q.ScaleMax = nil, 0
		}
		if q.ResultGroup != "" && !q.scored() {
			fields[at("resultGroup")] = "only SCALE, NUMERIC and SINGLE questions with scored options feed a result group"
		}
		out[i] = q
	}
	return out, fields
}

// NormalizeAnswers checks answers against the questions and returns each as
// a string (SINGLE, SCALE, NUMERIC, TEXT, DATE) or a sorted []string (MULTI).
func NormalizeAnswers(questions []Question, raw map[string]json.RawMessage) (map[string]any, map[string]string) {
	fields := map[string]string{}
	byCode := map[string]Question{}
	for _, q := range questions {
		byCode[q.Code] = q
	}
	for code := range raw {
		if _, ok := byCode[code]; !ok {
			fields["answers."+code] = "is not a question of this questionnaire"
		}
	}
	out := map[string]any{}
	for _, q := range questions {
		value, ok := raw[q.Code]
		if !ok || string(value) == "null" {
			if q.Mandatory {
				fields["answers."+q.Code] = "is required"
			}
			continue
		}
		v, msg := normalizeAnswer(q, value)
		if msg != "" {
			fields["answers."+q.Code] = msg
			continue
		}
		out[q.Code] = v
	}
	return out, fields
}

func normalizeAnswer(q Question, raw json.RawMessage) (any, string) {
	switch q.Type {
	case QuestionMulti:
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil || len(values) == 0 {
			return nil, "must be a non-empty list of option values"
		}
		sort.Strings(values)
		for i, v := range values {
			if !hasOption(q, v) || (i > 0 && values[i-1] == v) {
				return nil, "must list distinct option values of the question"
			}
		}
		return values, ""
	case QuestionNumeric:
		d, err := decimal.NewFromString(string(raw))
		if err != nil || d.Abs().GreaterThan(scoreBound) {
			return nil, "must be a JSON number"
		}
		return d.String(), ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		var n int
		if q.Type != QuestionScale || json.Unmarshal(raw, &n) != nil {
			return nil, "has the wrong type for a " + q.Type + " question"
		}
		s = fmt.Sprint(n)
	}
	switch q.Type {
	case QuestionSingle:
		if !hasOption(q, s) {
			return nil, "must be an option value of the question"
		}
	case QuestionScale:
		n, err := strconv.Atoi(s)
		if err != nil || n < 1 || n > q.ScaleMax || strconv.Itoa(n) != s {
			return nil, fmt.Sprintf("must be a whole number from 1 to %d", q.ScaleMax)
		}
	case QuestionText:
		if n := utf8.RuneCountInString(s); n < 1 || n > maxAnswerText {
			return nil, "must be 1 to 2000 characters"
		}
	case QuestionDate:
		if _, err := time.Parse(dateLayout, s); err != nil {
			return nil, "must be a YYYY-MM-DD date"
		}
	}
	return s, ""
}

const dateLayout = "2006-01-02"

func hasOption(q Question, v string) bool {
	for _, o := range q.Options {
		if o.Value == v {
			return true
		}
	}
	return false
}

func inList(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// answerValue is the number a scored answer contributes.
func answerValue(q Question, answer any) (decimal.Decimal, bool) {
	s, ok := answer.(string)
	if !ok {
		return decimal.Zero, false
	}
	if q.Type == QuestionSingle {
		for _, o := range q.Options {
			if o.Value == s && o.Score != nil {
				d, err := decimal.NewFromString(*o.Score)
				return d, err == nil
			}
		}
		return decimal.Zero, false
	}
	d, err := decimal.NewFromString(s)
	return d, err == nil
}

// Score is BR-42.10: per result group, Σ(answer value × weight) ÷ Σ weights
// over the scored questions the respondent answered, rounded to 2 decimals.
func Score(questions []Question, answers map[string]any) map[string]string {
	num, den := map[string]decimal.Decimal{}, map[string]decimal.Decimal{}
	for _, q := range questions {
		if q.ResultGroup == "" {
			continue
		}
		answer, answered := answers[q.Code]
		if !answered {
			continue
		}
		value, ok := answerValue(q, answer)
		if !ok {
			continue
		}
		w, _ := decimal.NewFromString(q.Weight)
		num[q.ResultGroup] = num[q.ResultGroup].Add(value.Mul(w))
		den[q.ResultGroup] = den[q.ResultGroup].Add(w)
	}
	out := map[string]string{}
	for group, n := range num {
		out[group] = n.Div(den[group]).Round(2).StringFixed(2)
	}
	return out
}

// Session is one stored answer session, decoded for the results analysis.
type Session struct {
	Answers map[string]any
	Scores  map[string]string
}

type QuestionStat struct {
	Code         string         `json:"code"`
	Type         string         `json:"type"`
	Answered     int            `json:"answered"`
	Mean         *string        `json:"mean,omitempty"`
	Distribution map[string]int `json:"distribution,omitempty"`
}

type GroupStat struct {
	Code      string  `json:"code"`
	Responses int     `json:"responses"`
	MeanScore *string `json:"meanScore,omitempty"`
}

type Stats struct {
	Respondents  int            `json:"respondents"`
	Questions    []QuestionStat `json:"questions"`
	ResultGroups []GroupStat    `json:"resultGroups"`
}

// Aggregate summarises sessions by question and by result group. Free text is
// counted, never returned.
func Aggregate(questions []Question, sessions []Session) Stats {
	st := Stats{Respondents: len(sessions), Questions: []QuestionStat{}, ResultGroups: []GroupStat{}}
	for _, q := range questions {
		qs := QuestionStat{Code: q.Code, Type: q.Type}
		if q.Type == QuestionSingle || q.Type == QuestionMulti || q.Type == QuestionScale {
			qs.Distribution = map[string]int{}
		}
		sum := decimal.Zero
		numeric := 0
		for _, s := range sessions {
			answer, ok := s.Answers[q.Code]
			if !ok {
				continue
			}
			qs.Answered++
			for _, v := range answerStrings(answer) {
				if qs.Distribution != nil {
					qs.Distribution[v]++
				}
			}
			if v, ok := answerValue(q, answer); ok && q.scored() {
				sum, numeric = sum.Add(v), numeric+1
			}
		}
		if numeric > 0 {
			m := sum.Div(decimal.NewFromInt(int64(numeric))).Round(2).StringFixed(2)
			qs.Mean = &m
		}
		st.Questions = append(st.Questions, qs)
	}
	totals, counts := map[string]decimal.Decimal{}, map[string]int{}
	for _, s := range sessions {
		for group, score := range s.Scores {
			d, err := decimal.NewFromString(score)
			if err != nil {
				continue
			}
			totals[group], counts[group] = totals[group].Add(d), counts[group]+1
		}
	}
	groups := make([]string, 0, len(counts))
	for g := range counts {
		groups = append(groups, g)
	}
	sort.Strings(groups)
	for _, g := range groups {
		m := totals[g].Div(decimal.NewFromInt(int64(counts[g]))).Round(2).StringFixed(2)
		st.ResultGroups = append(st.ResultGroups, GroupStat{Code: g, Responses: counts[g], MeanScore: &m})
	}
	return st
}

func answerStrings(answer any) []string {
	switch v := answer.(type) {
	case string:
		return []string{v}
	case []string:
		return v
	case []any:
		out := make([]string, 0, len(v))
		for _, x := range v {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// CheckBreakdown is the BR-42.10 anonymity rule: a breakdown of an anonymous
// questionnaire is refused when any group has fewer than the threshold of
// respondents. Refusing the whole breakdown also stops a hidden small group
// from being worked out by subtracting the visible ones from the total.
func CheckBreakdown(anonymous bool, groupSizes []int) *Error {
	if !anonymous {
		return nil
	}
	for _, n := range groupSizes {
		if n < AnonymityThreshold {
			return &Error{Code: CodeQuestionnaireThreshold, Detail: fmt.Sprintf("breakdown hidden: fewer than %d respondents in a group; use a wider group", AnonymityThreshold)}
		}
	}
	return nil
}
