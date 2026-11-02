package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/dynamodb/attributevalue"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/gorilla/mux"
)

const tableName = "rsimkhad-weatherAlert"

type StatusResponse struct {
	Table       string `json:"table"`
	RecordCount int64  `json:"recordCount"`
}

type Alert struct {
	Area     string `json:"Area" dynamodbav:"Area"`
	Event    string `json:"Event" dynamodbav:"Event"`
	Expires  string `json:"Expires" dynamodbav:"Expires"`
	Headline string `json:"Headline" dynamodbav:"Headline"`
	ID       string `json:"ID" dynamodbav:"ID"`
	Severity string `json:"Severity" dynamodbav:"Severity"`
}

type WeatherPoll struct {
	PollID     string  `json:"PollID" dynamodbav:"PollID"`
	PollTime   string  `json:"PollTime" dynamodbav:"PollTime"`
	AlertCount int     `json:"AlertCount" dynamodbav:"AlertCount"`
	Alerts     []Alert `json:"Alerts" dynamodbav:"Alerts"`
}

var db *dynamodb.Client

func main() {
	lakerID := strings.TrimSpace(os.Getenv("LAKER_ID"))
	if lakerID == "" {
		log.Fatal("LAKER_ID environment variable is required")
	}

	logglyToken := strings.TrimSpace(os.Getenv("LOGGLY_TOKEN"))
	if logglyToken == "" {
		log.Println("WARNING: LOGGLY_TOKEN is not set; logs will only appear in the console")
	}

	// Load AWS configuration.
	// The application uses the us-east-1 region.
	cfg, err := config.LoadDefaultConfig(
		context.Background(),
		config.WithRegion("us-east-1"),
	)
	if err != nil {
		log.Fatalf("Unable to load AWS configuration: %v", err)
	}

	// Create DynamoDB client.
	db = dynamodb.NewFromConfig(cfg)

	// Create Gorilla Mux router.
	router := mux.NewRouter()

	// Register API endpoints.
	router.HandleFunc("/"+lakerID+"/all", getAllRecords).Methods(http.MethodGet)
	router.HandleFunc("/"+lakerID+"/status", getTableStatus).Methods(http.MethodGet)
	router.HandleFunc("/"+lakerID+"/search", searchRecords).Methods(http.MethodGet)

	// Log every incoming request.
	handler := loggingMiddleware(logglyToken, router)

	port := strings.TrimSpace(os.Getenv("PORT"))
	if port == "" {
		port = "8080"
	}

	log.Printf("Server listening on port %s", port)
	log.Printf("All endpoint: /%s/all", lakerID)
	log.Printf("Status endpoint: /%s/status", lakerID)
	log.Printf("Search endpoint: /%s/search", lakerID)

	log.Fatal(http.ListenAndServe(":"+port, handler))
}

// GET /<laker_id>/all
// Returns all records from DynamoDB.
func getAllRecords(w http.ResponseWriter, r *http.Request) {
	records, err := fetchAllRecords(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Unable to retrieve records from DynamoDB")
		log.Printf("DynamoDB scan error: %v", err)
		return
	}

	writeJSON(w, http.StatusOK, records)
}

// GET /<laker_id>/status
// Returns table name and record count.
func getTableStatus(w http.ResponseWriter, r *http.Request) {
	result, err := db.DescribeTable(
		r.Context(),
		&dynamodb.DescribeTableInput{
			TableName: aws.String(tableName),
		},
	)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Unable to retrieve table status")
		log.Printf("DescribeTable error: %v", err)
		return
	}

	var count int64
	if result.Table != nil && result.Table.ItemCount != nil {
		count = *result.Table.ItemCount
	}

	response := StatusResponse{
		Table:       tableName,
		RecordCount: count,
	}

	writeJSON(w, http.StatusOK, response)
}

// GET /<laker_id>/search?PollID=value
// GET /<laker_id>/search?PollTime=value
// GET /<laker_id>/search?Severity=value
//
// Multiple supported parameters can be combined.
func searchRecords(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()

	allowedParams := map[string]bool{
		"PollID":   true,
		"PollTime": true,
		"Severity": true,
	}

	if len(params) == 0 {
		writeError(w, http.StatusBadRequest,
			"Provide at least one search parameter: PollID, PollTime, or Severity")
		return
	}

	// Reject unknown parameters and validate values.
	for key, values := range params {
		if !allowedParams[key] {
			writeError(w, http.StatusBadRequest,
				"Unsupported search parameter: "+key)
			return
		}

		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			writeError(w, http.StatusBadRequest,
				"Each search parameter must have exactly one non-empty value")
			return
		}

		value := values[0]

		if len(value) > 100 {
			writeError(w, http.StatusBadRequest,
				"Search parameter values cannot exceed 100 characters")
			return
		}

		switch key {
		case "PollID":
			if !isSafeIdentifier(value) {
				writeError(w, http.StatusBadRequest,
					"PollID contains invalid characters")
				return
			}

		case "PollTime":
			if !isSafeTimePrefix(value) {
				writeError(w, http.StatusBadRequest,
					"PollTime contains invalid characters")
				return
			}

		case "Severity":
			if !isSafeText(value) {
				writeError(w, http.StatusBadRequest,
					"Severity contains invalid characters")
				return
			}
		}
	}

	// Retrieve records from DynamoDB.
	records, err := fetchAllRecords(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError,
			"Unable to search DynamoDB records")
		log.Printf("DynamoDB search scan error: %v", err)
		return
	}

	// Filter records using the supplied search parameters.
	var matches []WeatherPoll

	for _, record := range records {
		if matchesSearch(record, params) {
			matches = append(matches, record)
		}
	}

	if matches == nil {
		matches = []WeatherPoll{}
	}

	writeJSON(w, http.StatusOK, matches)
}

// Fetch every DynamoDB record, handling pagination.
func fetchAllRecords(ctx context.Context) ([]WeatherPoll, error) {
	var records []WeatherPoll

	var lastKey map[string]types.AttributeValue

	for {
		result, err := db.Scan(
			ctx,
			&dynamodb.ScanInput{
				TableName:         aws.String(tableName),
				ExclusiveStartKey: lastKey,
			},
		)
		if err != nil {
			return nil, err
		}

		var page []WeatherPoll

		err = attributevalue.UnmarshalListOfMaps(
			result.Items,
			&page,
		)
		if err != nil {
			return nil, err
		}

		records = append(records, page...)

		if len(result.LastEvaluatedKey) == 0 {
			break
		}

		lastKey = result.LastEvaluatedKey
	}

	if records == nil {
		records = []WeatherPoll{}
	}

	return records, nil
}

// Check whether a record matches all supplied search parameters.
func matchesSearch(record WeatherPoll, params map[string][]string) bool {
	if value, ok := params["PollID"]; ok {
		if record.PollID != value[0] {
			return false
		}
	}

	if value, ok := params["PollTime"]; ok {
		if !strings.HasPrefix(record.PollTime, value[0]) {
			return false
		}
	}

	if value, ok := params["Severity"]; ok {
		found := false

		for _, alert := range record.Alerts {
			if strings.EqualFold(alert.Severity, value[0]) {
				found = true
				break
			}
		}

		if !found {
			return false
		}
	}

	return true
}

// Validate PollID characters.
func isSafeIdentifier(value string) bool {
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') ||
			char == '-' || char == '_') {
			return false
		}
	}

	return true
}

// Validate PollTime prefix characters.
func isSafeTimePrefix(value string) bool {
	for _, char := range value {
		if !((char >= '0' && char <= '9') ||
			char == 'T' || char == 'Z' ||
			char == ':' || char == '.' ||
			char == '+' || char == '-') {
			return false
		}
	}

	return true
}

// Validate text search values.
func isSafeText(value string) bool {
	for _, char := range value {
		if !((char >= 'a' && char <= 'z') ||
			(char >= 'A' && char <= 'Z') ||
			char == ' ' || char == '-') {
			return false
		}
	}

	return true
}

// Write a JSON response.
func writeJSON(w http.ResponseWriter, statusCode int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)

	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("JSON encoding error: %v", err)
	}
}

// Write a JSON error response.
func writeError(w http.ResponseWriter, statusCode int, message string) {
	writeJSON(w, statusCode, map[string]string{
		"error": message,
	})
}

// Capture HTTP response status for request logging.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (recorder *statusRecorder) WriteHeader(statusCode int) {
	recorder.status = statusCode
	recorder.ResponseWriter.WriteHeader(statusCode)
}

func (recorder *statusRecorder) Write(data []byte) (int, error) {
	if recorder.status == 0 {
		recorder.WriteHeader(http.StatusOK)
	}

	return recorder.ResponseWriter.Write(data)
}

// Log every request to the console and Loggly.
func loggingMiddleware(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{
			ResponseWriter: w,
		}

		next.ServeHTTP(recorder, r)

		statusCode := recorder.status
		if statusCode == 0 {
			statusCode = http.StatusOK
		}

		logRequest(token, r, statusCode)
	})
}

// Existing Loggly logging functionality.
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

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

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
