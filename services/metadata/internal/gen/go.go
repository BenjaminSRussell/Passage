package gen

import (
	"fmt"
	"strings"

	"github.com/BenjaminSRussell/passage/services/metadata/internal/domain"
)

func goRuntime(entity *domain.Entity) (string, string, string) {
	return goDomain(entity), goRepository(entity), goHandlers(entity)
}

func goDomain(entity *domain.Entity) string {
	var builder strings.Builder
	builder.WriteString("package generated\n\n")
	fmt.Fprintf(&builder, "type %s struct {\n\tID string `json:\"id\"`\n\tTenantID string `json:\"tenantId\"`\n", entity.Name)
	if entity.PerLegalEntity {
		builder.WriteString("\tLegalEntityID string `json:\"legalEntityId\"`\n")
	}
	for _, field := range entity.Fields {
		for _, property := range props(entity, field) {
			kind := goType(field.Type)
			if property.Col != field.Name {
				kind = "string"
			}
			if !field.Required {
				kind = "*" + kind
			}
			fmt.Fprintf(&builder, "\t%s %s `json:\"%s,omitempty\"`\n", goName(property.Col), kind, property.Wire)
		}
	}
	builder.WriteString("\tExt map[string]any `json:\"ext\"`\n\tVersion int `json:\"version\"`\n}\n")
	return builder.String()
}

func goRepository(entity *domain.Entity) string {
	name := entity.Name
	return fmt.Sprintf(`package generated

import "context"

type %[1]sRepository interface {
	List(context.Context, int) ([]%[1]s, error)
	Get(context.Context, string) (%[1]s, error)
	Create(context.Context, %[1]s) (%[1]s, error)
	Update(context.Context, string, map[string]any, int) (%[1]s, error)
	Delete(context.Context, string, int) error
}
`, name)
}

func goHandlers(entity *domain.Entity) string {
	name := entity.Name
	path := collectionPath(entity)
	return fmt.Sprintf(`package generated

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"
	"github.com/BenjaminSRussell/passage/platform/httpx"
)

func Register%[1]sRoutes(router chi.Router, repository %[1]sRepository) {
	router.Get("%[2]s", func(w http.ResponseWriter, r *http.Request) {
		rows, err := repository.List(r.Context(), 100)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusOK, rows)
	})
	router.Post("%[2]s", func(w http.ResponseWriter, r *http.Request) {
		var input %[1]s
		if err := httpx.Decode(r, &input); err != nil { httpx.Fail(w, r, err); return }
		row, err := repository.Create(r.Context(), input)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusCreated, row)
	})
	router.Patch("%[2]s/{id}", func(w http.ResponseWriter, r *http.Request) {
		var patch map[string]any
		if err := httpx.Decode(r, &patch); err != nil { httpx.Fail(w, r, err); return }
		version, err := strconv.Atoi(r.Header.Get("If-Match"))
		if err != nil { httpx.Fail(w, r, httpx.NewProblem(428, "MET-0008", "If-Match is required")); return }
		row, err := repository.Update(r.Context(), chi.URLParam(r, "id"), patch, version)
		if err != nil { httpx.Fail(w, r, err); return }
		httpx.JSON(w, http.StatusOK, row)
	})
}
`, name, path)
}

func goName(name string) string {
	parts := strings.Split(name, "_")
	for i := range parts {
		parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
	}
	return strings.Join(parts, "")
}

func goType(kind string) string {
	switch kind {
	case "bool":
		return "bool"
	case "int32":
		return "int32"
	case "int64":
		return "int64"
	case "json":
		return "any"
	case "bytes":
		return "[]byte"
	}
	return "string"
}
