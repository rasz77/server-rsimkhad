package main

import (
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"
)

type StatusResponse struct {
	Time string `json:"time"`
}

func main() {
	lakerID := strings.TrimSpace(os.Getenv("LAKER_ID"))
	if lakerID == "" {
		log.Fatal("LAKER_ID environment variable is required")
	}

	logglyToken := strings.TrimSpace(os.Getenv("LOGGLY_TOKEN"))
	if logglyToken == "" {
		log.Println("WARNING: LOGGLY_TOKEN is not set; request logs will only appear in the console")
	}

	statusPath := "/" + lakerID + "/status"
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		statusCode := http.StatusOK

		// Every method other than GET returns 405.
		if r.Method != http.MethodGet {
			statusCode = http.StatusMethodNotAllowed
			writeError(w, statusCode, "Method Not Allowed")
			logRequest(logglyToken, r, statusCode)
			return
		}

		// GET requests to any other path return 404.
		if r.URL.Path != statusPath {
			statusCode = http.StatusNotFound
			writeError(w, statusCode, "Not Found")
			logRequest(logglyToken, r, statusCode)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(StatusResponse{
			Time: time.Now().Format(time.RFC3339Nano),
		})

		logRequest(logglyToken, r, statusCode)
	})

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	log.Printf("Server listening on port %s", port)
	log.Printf("Status endpoint: %s", statusPath)

	log.Fatal(http.ListenAndServe(":"+port, mux))
}

func writeError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(map[string]string{
		"error": message,
	})
}

func logRequest(token string, r *http.Request, statusCode int) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	message := fmt.Sprintf(
		"method=%s source_ip=%s path=%s status=%d",
		r.Method,
		host,
		r.URL.Path,
		statusCode,
	)

	log.Println(message)

	if token == "" {
		return
	}

	logglyURL := "https://logs-01.loggly.com/inputs/" +
		token + "/tag/status-server/"

	req, err := http.NewRequest(
		http.MethodPost,
		logglyURL,
		strings.NewReader(message),
	)
	if err != nil {
		log.Printf("ERROR creating Loggly request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "text/plain")

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("ERROR sending request log to Loggly: %v", err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		log.Printf("ERROR: Loggly returned status %s", resp.Status)
	}
}
