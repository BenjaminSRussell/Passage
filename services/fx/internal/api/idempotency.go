package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5/middleware"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/fx/internal/store"
)

const (
	idempotencyHeader = "Idempotency-Key"
	maxKeyLength      = 255
	maxBodyBytes      = 4 << 20
)

func requestHash(method, path string, body []byte) string {
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
		if !validTenant(w, r) {
			return
		}
		key := r.Header.Get(idempotencyHeader)
		if key == "" || len(key) > maxKeyLength {
			httpx.Fail(w, r, badRequest(map[string]string{idempotencyHeader: "is required and must be 1 to 255 characters"}))
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
		if err != nil {
			httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body unreadable or too large"))
			return
		}
		r.Body = io.NopCloser(bytes.NewReader(raw))
		hash := requestHash(r.Method, r.URL.Path, raw)
		rec, found, err := a.store.GetIdempotency(r.Context(), key)
		if err != nil {
			a.fail(w, r, "idempotency lookup failed", err)
			return
		}
		if found {
			a.replay(w, r, rec, hash)
			return
		}
		buf := &bufferWriter{header: http.Header{}}
		next(buf, r)
		if buf.status == 0 {
			buf.status = http.StatusOK
		}
		if buf.status >= 200 && buf.status < 300 {
			rec := store.IdempotencyRecord{Key: key, RequestHash: hash, ResponseStatus: buf.status, ResponseBody: buf.body.Bytes()}
			if err := a.store.PutIdempotency(r.Context(), rec); err != nil {
				slog.Error("idempotency record failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			}
		}
		for name, values := range buf.header {
			w.Header()[name] = values
		}
		w.WriteHeader(buf.status)
		_, _ = w.Write(buf.body.Bytes())
	}
}

func (a *API) replay(w http.ResponseWriter, r *http.Request, rec store.IdempotencyRecord, hash string) {
	if rec.RequestHash != hash {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusUnprocessableEntity, "COM-0409", "idempotency key reused with a different request"))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Idempotent-Replayed", "true")
	w.WriteHeader(rec.ResponseStatus)
	_, _ = w.Write(rec.ResponseBody)
}
