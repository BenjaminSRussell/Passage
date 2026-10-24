package generated

import "context"

type JournalLineStubRepository interface {
	List(context.Context, int) ([]JournalLineStub, error)
	Get(context.Context, string) (JournalLineStub, error)
	Create(context.Context, JournalLineStub) (JournalLineStub, error)
	Update(context.Context, string, map[string]any, int) (JournalLineStub, error)
	Delete(context.Context, string, int) error
}
