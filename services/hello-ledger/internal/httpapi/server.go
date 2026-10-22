package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"strconv"

	"github.com/BenjaminSRussell/Passage/services/hello-ledger/internal/ledger"
)

const (
	defaultPageLimit = 50
	maxPageLimit     = 100
)

// Server is the ledger HTTP API. Journal routes require a bearer token.
type Server struct {
	book  *ledger.Book
	token string
}

func NewServer(token string) *Server {
	return newServer(token, nil)
}

func newServer(token string, book *ledger.Book) *Server {
	if book == nil {
		book = ledger.NewBook()
	}
	return &Server{book: book, token: token}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/entries", s.postEntry)
	mux.HandleFunc("GET /v1/entries", s.listEntries)
	mux.HandleFunc("GET /v1/trial-balance", s.trialBalance)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Path) >= 4 && r.URL.Path[:4] == "/v1/" && !s.authorized(r) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func (s *Server) authorized(r *http.Request) bool {
	if s.token == "" {
		return false
	}
	header := r.Header.Get("Authorization")
	const prefix = "Bearer "
	if len(header) <= len(prefix) || header[:len(prefix)] != prefix {
		return false
	}
	got := header[len(prefix):]
	if len(got) != len(s.token) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(s.token)) == 1
}

func (s *Server) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"status":  "ok",
		"service": "hello-ledger",
	})
}

type lineDTO struct {
	Account string `json:"account"`
	Debit   int64  `json:"debit"`
	Credit  int64  `json:"credit"`
}

type entryDTO struct {
	ID    int64     `json:"id"`
	Lines []lineDTO `json:"lines"`
}

func (s *Server) postEntry(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Lines []lineDTO `json:"lines"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	lines := make([]ledger.Line, len(body.Lines))
	for i, line := range body.Lines {
		lines[i] = ledger.Line{Account: line.Account, Debit: line.Debit, Credit: line.Credit}
	}
	entry, err := s.book.Post(lines)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, ledger.ErrCapacity) {
			status = http.StatusConflict
		} else if !errors.Is(err, ledger.ErrUnbalanced) && !errors.Is(err, ledger.ErrInvalidLine) {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toEntryDTO(entry))
}

func (s *Server) listEntries(w http.ResponseWriter, r *http.Request) {
	offset, limit, err := pageQuery(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	entries, total := s.book.EntriesPage(offset, limit)
	out := make([]entryDTO, len(entries))
	for i, entry := range entries {
		out[i] = toEntryDTO(entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"entries": out,
		"total":   total,
		"offset":  offset,
		"limit":   limit,
	})
}

func pageQuery(r *http.Request) (int, int, error) {
	offset := 0
	limit := defaultPageLimit
	if raw := r.URL.Query().Get("offset"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 0 {
			return 0, 0, errors.New("invalid offset")
		}
		offset = parsed
	}
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageLimit {
			return 0, 0, errors.New("invalid limit")
		}
		limit = parsed
	}
	return offset, limit, nil
}

func (s *Server) trialBalance(w http.ResponseWriter, _ *http.Request) {
	balances := s.book.TrialBalance()
	type row struct {
		Account string `json:"account"`
		Balance int64  `json:"balance"`
	}
	rows := make([]row, len(balances))
	total := new(big.Int)
	for i, balance := range balances {
		rows[i] = row{Account: balance.Account, Balance: balance.Balance}
		total.Add(total, big.NewInt(balance.Balance))
	}
	if !total.IsInt64() {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "trial balance total overflow"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": rows, "total": total.Int64()})
}

func toEntryDTO(entry ledger.Entry) entryDTO {
	lines := make([]lineDTO, len(entry.Lines))
	for i, line := range entry.Lines {
		lines[i] = lineDTO{Account: line.Account, Debit: line.Debit, Credit: line.Credit}
	}
	return entryDTO{ID: entry.ID, Lines: lines}
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
