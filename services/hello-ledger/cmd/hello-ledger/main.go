package main

import (
	"log"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/Passage/services/hello-ledger/internal/httpapi"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	addr := ":" + port
	log.Printf("hello-ledger listening on %s", addr)
	if err := http.ListenAndServe(addr, httpapi.NewServer().Handler()); err != nil {
		log.Fatal(err)
	}
}
