// Command healthcheck is the container health check: it asks the health endpoint of the application and exits 1
// when it does not answer 200. The image is built from scratch, so there is no curl or shell to do it with.
package main

import (
	"fmt"
	"net/http"
	"os"
	"time"
)

func main() {
	url := os.Getenv("HEALTHCHECK_URL")
	if url == "" {
		port := os.Getenv("HTTP_SERVER_PORT")
		if port == "" {
			port = "8080"
		}
		url = "http://localhost:" + port + "/api/v1/healthz"
	}

	client := http.Client{Timeout: 3 * time.Second}

	resp, err := client.Get(url)
	if err != nil {
		fmt.Fprintln(os.Stderr, "health check failed:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "health check failed: status", resp.StatusCode)
		os.Exit(1)
	}
}
