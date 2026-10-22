// Package httpapi serves the hello-ledger placeholder HTTP API.
package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/BenjaminSRussell/Passage/services/hello-ledger/internal/ledger"
)

// Server is the placeholder ledger API.
type Server struct {
	book *ledger.Book
}

func NewServer() *Server {
	return &Server{book: ledger.NewBook()}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("POST /v1/entries", s.postEntry)
	mux.HandleFunc("GET /v1/entries", s.listEntries)
	mux.HandleFunc("GET /v1/trial-balance", s.trialBalance)
	return mux
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
		if !errors.Is(err, ledger.ErrUnbalanced) && !errors.Is(err, ledger.ErrInvalidLine) {
			status = http.StatusInternalServerError
		}
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusCreated, toEntryDTO(entry))
}

func (s *Server) listEntries(w http.ResponseWriter, _ *http.Request) {
	entries := s.book.Entries()
	out := make([]entryDTO, len(entries))
	for i, entry := range entries {
		out[i] = toEntryDTO(entry)
	}
	writeJSON(w, http.StatusOK, map[string]any{"entries": out})
}

func (s *Server) trialBalance(w http.ResponseWriter, _ *http.Request) {
	balances := s.book.TrialBalance()
	type row struct {
		Account string `json:"account"`
		Balance int64  `json:"balance"`
	}
	rows := make([]row, len(balances))
	var total int64
	for i, balance := range balances {
		rows[i] = row{Account: balance.Account, Balance: balance.Balance}
		total += balance.Balance
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": rows, "total": total})
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
