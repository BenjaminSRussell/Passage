package generated

import "context"

type NoteRepository interface {
	List(context.Context, int) ([]Note, error)
	Get(context.Context, string) (Note, error)
	Create(context.Context, Note) (Note, error)
	Update(context.Context, string, map[string]any, int) (Note, error)
	Delete(context.Context, string, int) error
}
