package main

import (
	"log"
	"net/http"
	"os"
	"time"

	"github.com/BenjaminSRussell/Passage/services/hello-ledger/internal/httpapi"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	bind := os.Getenv("BIND_ADDR")
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := bind + ":" + port
	server := &http.Server{
		Addr:              addr,
		Handler:           httpapi.NewServer(os.Getenv("LEDGER_TOKEN")).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("hello-ledger listening on %s", addr)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
