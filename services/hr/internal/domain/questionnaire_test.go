package domain

import (
	"encoding/json"
	"testing"
)

func score(v string) *string { return &v }

func sampleQuestions(t *testing.T) []Question {
	t.Helper()
	qs, fields := NormalizeQuestions([]Question{
		{Code: "Q1", Text: "I would recommend this team", Type: QuestionScale, ResultGroup: "ENGAGE", Weight: "2", Mandatory: true},
		{Code: "Q2", Text: "Workload", Type: QuestionSingle, ResultGroup: "ENGAGE",
			Options: []Option{{Value: "LOW", Label: "Low", Score: score("1")}, {Value: "HIGH", Label: "High", Score: score("5")}}},
		{Code: "Q3", Text: "Perks", Type: QuestionMulti, Options: []Option{{Value: "GYM", Label: "Gym"}, {Value: "BUS", Label: "Bus"}}},
		{Code: "Q4", Text: "Comments", Type: QuestionText},
	})
	if len(fields) > 0 {
		t.Fatalf("design rejected: %v", fields)
	}
	return qs
}

func TestNormalizeQuestionsDefaultsAndRejects(t *testing.T) {
	qs := sampleQuestions(t)
	if qs[0].ScaleMax != 5 || qs[2].Weight != "1" {
		t.Errorf("defaults not applied: %+v", qs)
	}
	_, fields := NormalizeQuestions([]Question{
		{Code: "A", Text: "x", Type: QuestionText, ResultGroup: "G"},
		{Code: "A", Text: "", Type: "ESSAY"},
		{Code: "B", Text: "x", Type: QuestionSingle, Options: []Option{{Value: "ONE", Label: "One"}}},
		{Code: "C", Text: "x", Type: QuestionScale, ScaleMax: 11, Weight: "0"},
	})
	for _, f := range []string{"questions[0].resultGroup", "questions[1].code", "questions[1].text", "questions[1].type", "questions[2].options", "questions[3].scaleMax", "questions[3].weight"} {
		if fields[f] == "" {
			t.Errorf("field %s not rejected: %v", f, fields)
		}
	}
}

func rawAnswers(t *testing.T, doc string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(doc), &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestNormalizeAnswers(t *testing.T) {
	qs := sampleQuestions(t)
	got, fields := NormalizeAnswers(qs, rawAnswers(t, `{"Q1":4,"Q2":"HIGH","Q3":["GYM","BUS"],"Q4":"fine"}`))
	if len(fields) > 0 || got["Q1"] != "4" || got["Q3"].([]string)[0] != "BUS" {
		t.Fatalf("answers = %v fields = %v", got, fields)
	}
	for _, bad := range []string{`{}`, `{"Q1":6}`, `{"Q1":"4.0"}`, `{"Q1":3,"Q2":"MID"}`, `{"Q1":3,"Q3":["GYM","GYM"]}`, `{"Q1":3,"Q9":1}`, `{"Q1":3,"Q4":""}`} {
		if _, fields := NormalizeAnswers(qs, rawAnswers(t, bad)); len(fields) == 0 {
			t.Errorf("%s accepted", bad)
		}
	}
}

func TestScoreWeightedAverageRoundsToTwoPlaces(t *testing.T) {
	qs := sampleQuestions(t)
	// (4 x 2 + 5 x 1) / (2 + 1) = 4.33
	a, _ := NormalizeAnswers(qs, rawAnswers(t, `{"Q1":4,"Q2":"HIGH"}`))
	if got := Score(qs, a)["ENGAGE"]; got != "4.33" {
		t.Errorf("score = %s, want 4.33", got)
	}
	// An unanswered optional question drops out of both sums: 4 x 2 / 2.
	a, _ = NormalizeAnswers(qs, rawAnswers(t, `{"Q1":4}`))
	if got := Score(qs, a)["ENGAGE"]; got != "4.00" {
		t.Errorf("score without Q2 = %s, want 4.00", got)
	}
}

func TestAggregate(t *testing.T) {
	qs := sampleQuestions(t)
	var sessions []Session
	for _, doc := range []string{`{"Q1":4,"Q2":"HIGH","Q3":["GYM"]}`, `{"Q1":2,"Q2":"LOW","Q4":"x"}`} {
		a, f := NormalizeAnswers(qs, rawAnswers(t, doc))
		if len(f) > 0 {
			t.Fatal(f)
		}
		sessions = append(sessions, Session{Answers: a, Scores: Score(qs, a)})
	}
	st := Aggregate(qs, sessions)
	if st.Respondents != 2 || *st.Questions[0].Mean != "3.00" || st.Questions[0].Distribution["4"] != 1 {
		t.Errorf("Q1 stat = %+v", st.Questions[0])
	}
	if st.Questions[2].Distribution["GYM"] != 1 || st.Questions[3].Answered != 1 || st.Questions[3].Mean != nil {
		t.Errorf("Q3/Q4 stat = %+v %+v", st.Questions[2], st.Questions[3])
	}
	if len(st.ResultGroups) != 1 || st.ResultGroups[0].Responses != 2 || *st.ResultGroups[0].MeanScore != "3.00" {
		t.Errorf("groups = %+v", st.ResultGroups)
	}
}

func TestCheckBreakdown(t *testing.T) {
	if err := CheckBreakdown(true, []int{4}); err == nil || err.Code != CodeQuestionnaireThreshold {
		t.Errorf("4 respondents = %v, want %s", err, CodeQuestionnaireThreshold)
	}
	if err := CheckBreakdown(true, []int{12, 4}); err == nil {
		t.Error("a hidden small group must refuse the whole breakdown")
	}
	if err := CheckBreakdown(true, []int{5, 9}); err != nil {
		t.Errorf("groups at the threshold are shown: %v", err)
	}
	if err := CheckBreakdown(false, []int{1}); err != nil {
		t.Errorf("a named questionnaire has no threshold: %v", err)
	}
}
