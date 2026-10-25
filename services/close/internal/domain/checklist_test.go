package domain_test

import (
	"fmt"
	"testing"

	"github.com/BenjaminSRussell/passage/services/close/internal/domain"
)

func fortyTaskTemplate() []domain.TaskTemplate {
	template := make([]domain.TaskTemplate, 40)
	for i := range template {
		template[i] = domain.TaskTemplate{ID: fmt.Sprintf("t%02d", i), Name: fmt.Sprintf("Task %d", i)}
	}
	for i := 4; i < 40; i += 3 {
		template[i].DependsOn = []string{template[i-1].ID, template[i-4].ID}
	}
	return template
}

func TestChecklist_FortyTasksThreeEntities(t *testing.T) {
	template := fortyTaskTemplate()
	tasks, err := domain.Instantiate(template, []string{"US01", "DE01", "FR01"})
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 120 {
		t.Fatalf("got %d tasks, want 120", len(tasks))
	}
	dependent := 0
	for _, task := range tasks {
		if len(task.DependsOn) > 0 {
			dependent++
		}
	}
	if dependent != 3*12 {
		t.Fatalf("got %d tasks with dependencies, want 36", dependent)
	}
	completedAt := map[string]int{}
	for step := 0; len(completedAt) < len(tasks); step++ {
		progressed := false
		for _, task := range tasks {
			if _, done := completedAt[task.ID]; done || domain.Eligible(tasks, task.ID) != nil {
				continue
			}
			if err := domain.Complete(tasks, task.ID); err != nil {
				t.Fatal(err)
			}
			for _, d := range task.DependsOn {
				if _, ok := completedAt[d]; !ok {
					t.Fatalf("%s completed before dependency %s", task.ID, d)
				}
			}
			completedAt[task.ID] = step
			progressed = true
		}
		if !progressed {
			t.Fatalf("checklist stalled with %d of %d tasks completed", len(completedAt), len(tasks))
		}
	}
}

func TestComplete_BlockedByDependency(t *testing.T) {
	tasks, err := domain.Instantiate(fortyTaskTemplate(), []string{"US01"})
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Complete(tasks, "US01/t04"); code(err) != domain.CodeBlockedByDependency {
		t.Fatalf("got %v, want %s", err, domain.CodeBlockedByDependency)
	}
	if tasks[4].Status != domain.TaskPending {
		t.Fatal("a blocked task must stay pending")
	}
}

func TestSkip_SatisfiesDependencyAndRequiresReason(t *testing.T) {
	tasks, err := domain.Instantiate([]domain.TaskTemplate{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}, []string{"US01"})
	if err != nil {
		t.Fatal(err)
	}
	if err := domain.Skip(tasks, "US01/a", "  "); code(err) != domain.CodeValidation {
		t.Fatalf("blank reason must be rejected: %v", err)
	}
	if err := domain.Skip(tasks, "US01/a", "not applicable this period"); err != nil {
		t.Fatal(err)
	}
	if err := domain.Complete(tasks, "US01/b"); err != nil {
		t.Fatalf("a skipped dependency must unblock its dependents: %v", err)
	}
	if err := domain.Complete(tasks, "US01/b"); code(err) != domain.CodeInvalidTransition {
		t.Fatalf("completing twice must fail: %v", err)
	}
}

func TestValidateDAG_Table(t *testing.T) {
	tooMany := make([]domain.TaskTemplate, domain.MaxTemplateTasks+1)
	for i := range tooMany {
		tooMany[i].ID = fmt.Sprint(i)
	}
	tests := []struct {
		name     string
		template []domain.TaskTemplate
		wantErr  bool
	}{
		{"valid chain", []domain.TaskTemplate{{ID: "a"}, {ID: "b", DependsOn: []string{"a"}}}, false},
		{"cycle", []domain.TaskTemplate{{ID: "a", DependsOn: []string{"b"}}, {ID: "b", DependsOn: []string{"a"}}}, true},
		{"self dependency", []domain.TaskTemplate{{ID: "a", DependsOn: []string{"a"}}}, true},
		{"unknown dependency", []domain.TaskTemplate{{ID: "a", DependsOn: []string{"zz"}}}, true},
		{"duplicate id", []domain.TaskTemplate{{ID: "a"}, {ID: "a"}}, true},
		{"empty", nil, true},
		{"over 500 tasks", tooMany, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := domain.ValidateDAG(tc.template); (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestInstantiate_RequiresEntities(t *testing.T) {
	if _, err := domain.Instantiate([]domain.TaskTemplate{{ID: "a"}}, nil); err == nil {
		t.Fatal("empty entity list must be rejected")
	}
}
