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

	"github.com/BenjaminSRussell/passage/platform/fieldcrypt"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/receiving/internal/store"
)

const (
	idempotencyLock   = 30 * time.Second
	idempotencyHeader = "Idempotency-Key"
	maxKeyLength      = 255
	maxBodyBytes      = 4 << 20
)

// RequestHash is the fingerprint stored with an Idempotency-Key. With fieldcrypt keys it is
// "hmac:<key id>:" + HMAC-SHA256 under the tenant's MAC key (#335), so a stored row cannot confirm a
// guessed request body such as a one-time payee's name or IBAN. Without keys it is the plain SHA-256
// hex; no one-time payee can be stored then, and a refused request releases its key. A fieldcrypt
// rotation inside the 24-hour key lifetime makes a retry of an earlier request answer COM-0409.
func RequestHash(keys *fieldcrypt.Keys, tenantID, method, path string, body []byte) (string, error) {
	message := append([]byte(method+"\x00"+path+"\x00"), body...)
	if keys == nil {
		sum := sha256.Sum256(message)
		return hex.EncodeToString(sum[:]), nil
	}
	mac, err := keys.MAC(tenantID, message)
	if err != nil {
		return "", err
	}
	return "hmac:" + keys.CurrentID() + ":" + hex.EncodeToString(mac), nil
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
		principal, _ := tenant.FromContext(r.Context())
		hash, err := RequestHash(a.ports.FieldCrypt, principal.TenantID, r.Method, r.URL.Path, raw)
		if err != nil {
			slog.Error("idempotency request hash failed", "err", err, "correlationId", middleware.GetReqID(r.Context()))
			httpx.Fail(w, r, httpx.NewProblem(http.StatusInternalServerError, "COM-0500", "internal error"))
			return
		}
		claim, err := a.store.ClaimKey(r.Context(), key, hash, idempotencyLock)
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
