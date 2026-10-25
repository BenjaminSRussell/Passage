package domain

import "strings"

const MaxTemplateTasks = 500

type TaskStatus string

const (
	TaskPending   TaskStatus = "pending"
	TaskCompleted TaskStatus = "completed"
	TaskSkipped   TaskStatus = "skipped"
)

type TaskTemplate struct {
	ID        string
	Name      string
	DependsOn []string
}

type Task struct {
	ID         string
	TemplateID string
	Entity     string
	DependsOn  []string
	Status     TaskStatus
	Reason     string
}

func ValidateDAG(template []TaskTemplate) error {
	if len(template) == 0 || len(template) > MaxTemplateTasks {
		return fail(CodeValidation, "a template needs between 1 and 500 tasks")
	}
	indegree := make(map[string]int, len(template))
	for _, t := range template {
		if _, dup := indegree[t.ID]; dup || t.ID == "" {
			return fail(CodeValidation, "task ids must be unique and non-empty")
		}
		indegree[t.ID] = len(t.DependsOn)
	}
	dependents := map[string][]string{}
	for _, t := range template {
		for _, d := range t.DependsOn {
			if _, ok := indegree[d]; !ok {
				return fail(CodeValidation, "task "+t.ID+" depends on unknown task "+d)
			}
			dependents[d] = append(dependents[d], t.ID)
		}
	}
	var ready []string
	for id, n := range indegree {
		if n == 0 {
			ready = append(ready, id)
		}
	}
	visited := 0
	for len(ready) > 0 {
		id := ready[len(ready)-1]
		ready = ready[:len(ready)-1]
		visited++
		for _, next := range dependents[id] {
			if indegree[next]--; indegree[next] == 0 {
				ready = append(ready, next)
			}
		}
	}
	if visited != len(template) {
		return fail(CodeValidation, "task dependencies contain a cycle")
	}
	return nil
}

func Instantiate(template []TaskTemplate, entities []string) ([]Task, error) {
	if len(entities) == 0 {
		return nil, fail(CodeValidation, "at least one legal entity is required")
	}
	if err := ValidateDAG(template); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(template)*len(entities))
	for _, entity := range entities {
		for _, t := range template {
			deps := make([]string, len(t.DependsOn))
			for i, d := range t.DependsOn {
				deps[i] = entity + "/" + d
			}
			tasks = append(tasks, Task{ID: entity + "/" + t.ID, TemplateID: t.ID, Entity: entity, DependsOn: deps, Status: TaskPending})
		}
	}
	return tasks, nil
}

func find(tasks []Task, id string) (int, error) {
	for i := range tasks {
		if tasks[i].ID == id {
			return i, nil
		}
	}
	return 0, fail(CodeValidation, "unknown task "+id)
}

func Eligible(tasks []Task, id string) error {
	i, err := find(tasks, id)
	if err != nil {
		return err
	}
	if tasks[i].Status != TaskPending {
		return fail(CodeInvalidTransition, "task "+id+" is already "+string(tasks[i].Status))
	}
	for _, d := range tasks[i].DependsOn {
		j, err := find(tasks, d)
		if err != nil {
			return err
		}
		if tasks[j].Status == TaskPending {
			return fail(CodeBlockedByDependency, "task "+id+" is blocked by "+d)
		}
	}
	return nil
}

func Complete(tasks []Task, id string) error {
	return settle(tasks, id, TaskCompleted, "")
}

func Skip(tasks []Task, id, reason string) error {
	if strings.TrimSpace(reason) == "" {
		return fail(CodeValidation, "a skip reason is required")
	}
	return settle(tasks, id, TaskSkipped, reason)
}

func settle(tasks []Task, id string, status TaskStatus, reason string) error {
	if err := Eligible(tasks, id); err != nil {
		return err
	}
	i, _ := find(tasks, id)
	tasks[i].Status = status
	tasks[i].Reason = reason
	return nil
}
