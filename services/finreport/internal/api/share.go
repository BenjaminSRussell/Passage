package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/finreport/internal/store"
)

// DutyShare is "Share report library files" (FR-15.8): create, list and revoke
// share links. It is new in slice 19 and not seeded yet (NOT-IMPLEMENTED.md,
// authz seed).
const DutyShare = "reporting.library.share"

type createShareRequest struct {
	ExpiresInDays *int `json:"expiresInDays"`
}

// shareResponse is a link; on creation it also carries the secret token and
// the path that downloads the file with it.
type shareResponse struct {
	store.ShareLink
	Path string `json:"path,omitempty"`
}

// createShareLink makes a link to a library file. The Idempotency-Key header
// is required; the same request again returns the stored link (200, without
// its token, which was shown once).
func (a *API) createShareLink(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	libID, ok := libraryID(w, r)
	if !ok {
		return
	}
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0003", "Idempotency-Key header is required"))
		return
	}
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBody))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", "unreadable body"))
		return
	}
	var req createShareRequest
	if len(strings.TrimSpace(string(raw))) > 0 {
		if err := decodeStrict(raw, &req); err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0002", err.Error()))
			return
		}
	}
	days := a.shareDays
	if req.ExpiresInDays != nil {
		days = *req.ExpiresInDays
	}
	// The hash covers the library file and the lifetime the link ends up with,
	// so asking for the default days and for the same number explicitly agree.
	l, err := a.store.CreateShareLink(r.Context(), store.ShareSpec{LibraryID: libID, ExpiresInDays: days, Actor: p.UserID,
		IdempotencyKey: key, RequestHash: bodyHash([]byte(fmt.Sprintf("%s|%d", libID, days)))})
	if err != nil {
		fail(w, r, err)
		return
	}
	status := http.StatusCreated
	if l.Replayed {
		status = http.StatusOK
	}
	w.Header().Set("ETag", etag(l.Revision))
	out := shareResponse{ShareLink: l}
	if l.Token != "" {
		out.Path = "/v1/reporting/shared/" + l.Token
	}
	httpx.JSON(w, status, out)
}

func (a *API) listShareLinks(w http.ResponseWriter, r *http.Request) {
	if !principal(r) {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	libID, ok := libraryID(w, r)
	if !ok {
		return
	}
	items, err := a.store.ListShareLinks(r.Context(), libID)
	if err != nil {
		fail(w, r, err)
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"items": items})
}

// shareAction dispatches POST /share-links/{id}:revoke. If-Match carries the
// revision read.
func (a *API) shareAction(w http.ResponseWriter, r *http.Request) {
	p, ok := tenant.FromContext(r.Context())
	if !ok {
		httpx.Fail(w, r, unauthenticated())
		return
	}
	rest := chi.URLParam(r, "*")
	i := strings.LastIndex(rest, ":")
	if i < 0 || rest[i+1:] != "revoke" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, "COM-0404", "unknown share link action"))
		return
	}
	id := rest[:i]
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, store.CodeShareNotFound, "share link "+id+" does not exist"))
		return
	}
	ifMatch, err := parseIfMatch(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := a.store.RevokeShareLink(r.Context(), id, ifMatch, p.UserID)
	if err != nil {
		fail(w, r, err)
		return
	}
	w.Header().Set("ETag", etag(l.Revision))
	httpx.JSON(w, http.StatusOK, l)
}

// SharedPathPrefix is the path of the public share link download, followed by
// the token. cmd/finreport sends every request under it to PublicHandler and
// every other request to the platform router with the tenant middleware.
const SharedPathPrefix = "/v1/reporting/shared/"

// sharedPathRedacted is the download path as logged and echoed in problems:
// the token in the path is the credential, so it is never written anywhere.
const sharedPathRedacted = SharedPathPrefix + "{token}"

// PublicHandler serves the share link download and nothing else, with the
// platform router's request id, trusted-proxy client address, panic recovery
// and timeout, and an access log that leaves the token out of the path. No
// tenant middleware: the link's secret is the authority, and the store opens
// the link's tenant from the token (see store.RedeemShareLink).
func (a *API) PublicHandler(trustedProxies []netip.Prefix) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, httpx.TrustedRealIP(trustedProxies), middleware.Recoverer, middleware.Timeout(httpx.RequestTimeout), logSharedRequests)
	a.RegisterPublic(r)
	return r
}

func logSharedRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)
		slog.Info("http", "method", r.Method, "path", sharedPathRedacted, "status", ww.Status(), "bytes", ww.BytesWritten(),
			"durationMs", time.Since(start).Milliseconds(), "requestId", middleware.GetReqID(r.Context()))
	})
}

// sharedContent serves the file a share link names, with no Passage identity:
// the token is the authority. Every answer is uncacheable, never framed or run
// as a page (the stored file may be HTML), and the page that links here is not
// sent along as a referrer.
func (a *API) sharedContent(w http.ResponseWriter, r *http.Request) {
	h := w.Header()
	h.Set("Cache-Control", "private, no-store")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'; sandbox")
	h.Set("X-Frame-Options", "DENY")
	sf, err := a.store.RedeemShareLink(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		// httpx.Fail echoes the path as the problem's instance and logs it on
		// a 500; give it the path without the token.
		redacted := r.Clone(r.Context())
		redacted.URL.Path, redacted.URL.RawPath = sharedPathRedacted, ""
		httpx.Fail(w, redacted, err)
		return
	}
	slog.InfoContext(r.Context(), "share link download", "linkId", sf.LinkID, "libraryId", sf.Entry.ID,
		"clientAddr", r.RemoteAddr, "requestId", middleware.GetReqID(r.Context()))
	h.Set("Content-Type", sf.Entry.ContentType)
	h.Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, safeFileName(sf.Entry.FileName)))
	h.Set("ETag", `"`+sf.Entry.SHA256+`"`)
	h.Set("X-Passage-Content-Sha256", sf.Entry.SHA256)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(sf.Content)
}

func bodyHash(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
