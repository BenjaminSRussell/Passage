package api

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"

	"github.com/BenjaminSRussell/passage/platform/httpx"
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

func idempotencyKey(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.Header.Get(idempotencyHeader)
	if key == "" || len(key) > maxKeyLength {
		httpx.Fail(w, r, badRequest(map[string]string{idempotencyHeader: "is required and must be 1 to 255 characters"}))
		return "", false
	}
	return key, true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadRequest, "COM-0001", "request body unreadable or too large"))
		return nil, false
	}
	return raw, true
}
