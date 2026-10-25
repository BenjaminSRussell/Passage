package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	idempotencyLock   = 30 * time.Second
	idempotencyHeader = "Idempotency-Key"
	maxKeyLength      = 255
	maxBodyBytes      = 4 << 20
)

func RequestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

type bufferWriter struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (b *bufferWriter) Header() http.Header { return b.header }

func (b *bufferWriter) WriteHeader(status int) {
	if b.status == 0 {
		b.status = status
	}
}

func (b *bufferWriter) Write(p []byte) (int, error) {
	b.WriteHeader(http.StatusOK)
	return b.body.Write(p)
}

func (a *API) idempotent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get(idempotencyHeader)
		if key == "" || len(key) > maxKeyLength {
			problem := httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request failed validation")
			problem.Errors = map[string]string{idempotencyHeader: "is required and must be 1 to 255 characters"}
			httpx.Fail(w, r, problem)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body unreadable or too large"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		claim, err := a.store.ClaimKey(r.Context(), key, RequestHash(r.Method, r.URL.Path, raw), idempotencyLock)
		if err != nil {
			slog.Error("idempotency claim failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error"))
			return
		}
		switch claim.State {
		case store.ClaimMismatch:
			httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0409", "idempotency key reused with a different request"))
		case store.ClaimInFlight:
			w.Header().Set("Retry-After", "1")
			httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, "COM-0410", "a request with this idempotency key is still in flight"))
		case store.ClaimCompleted:
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(claim.Status)
			_, _ = w.Write(claim.Body)
		default:
			a.runClaimed(w, r, key, next)
		}
	}
}

func (a *API) runClaimed(w http.ResponseWriter, r *http.Request, key string, next http.HandlerFunc) {
	buf := &bufferWriter{header: http.Header{}}
	next(buf, r)
	if buf.status == 0 {
		buf.status = http.StatusOK
	}
	var err error
	if buf.status >= 200 && buf.status < 300 {
		err = a.store.CompleteKey(r.Context(), key, buf.status, buf.body.Bytes())
	} else {
		err = a.store.ReleaseKey(r.Context(), key)
	}
	if err != nil {
		slog.Error("idempotency key finalisation failed", "err", err, "status", buf.status, "correlationId", middleware.GetReqID(r.Context()))
	}
	for name, values := range buf.header {
		w.Header()[name] = values
	}
	w.WriteHeader(buf.status)
	_, _ = w.Write(buf.body.Bytes())
}
