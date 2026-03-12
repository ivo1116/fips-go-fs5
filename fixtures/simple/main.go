package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
)

func main() {
	// Test FIPS crypto
	h := sha256.New()
	h.Write([]byte("FIPS test data"))
	hash := fmt.Sprintf("%x", h.Sum(nil))

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "Hello from FIPS Go!\n")
		fmt.Fprintf(w, "SHA256 hash: %s\n", hash)
		fmt.Fprintf(w, "GOLANG_FIPS: %s\n", os.Getenv("GOLANG_FIPS"))
	})

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	fmt.Printf("Starting server on port %s\n", port)
	http.ListenAndServe(":"+port, nil)
}
