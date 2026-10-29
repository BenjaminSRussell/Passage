package api

import (
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// catalogEntryDTO is one pack of the workspace catalog (FR-49.1).
type catalogEntryDTO struct {
	Pack          string   `json:"pack"`
	SourceSystems []string `json:"sourceSystems"`
	ControlTotals string   `json:"controlTotals"`
	Available     bool     `json:"available"`
}

// packCatalog lists the nine transactional packs with the legacy systems they cover, the
// control totals each compares and whether this release runs it. It reads no tenant data.
func (s *Server) packCatalog(w http.ResponseWriter, r *http.Request) {
	if _, ok := principal(w, r); !ok {
		return
	}
	catalog := domain.PackCatalog()
	items := make([]catalogEntryDTO, 0, len(catalog))
	for _, e := range catalog {
		d := catalogEntryDTO{Pack: string(e.Pack), SourceSystems: make([]string, 0, len(e.Systems)), ControlTotals: e.ControlTotals, Available: e.Available}
		for _, sys := range e.Systems {
			d.SourceSystems = append(d.SourceSystems, string(sys))
		}
		items = append(items, d)
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}
