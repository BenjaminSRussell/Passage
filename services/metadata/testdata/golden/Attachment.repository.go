package generated

import "context"

type AttachmentRepository interface {
	List(context.Context, int) ([]Attachment, error)
	Get(context.Context, string) (Attachment, error)
	Create(context.Context, Attachment) (Attachment, error)
	Update(context.Context, string, map[string]any, int) (Attachment, error)
	Delete(context.Context, string, int) error
}
