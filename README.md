

# Weather Alert API Server

A REST API server built with Go and Docker. The server connects to an AWS DynamoDB table containing weather alert records and provides endpoints to retrieve, search, and check the status of stored records.

## API Endpoints

- **`GET /rsimkhad/status`** – Returns the DynamoDB table name and record count.
- **`GET /rsimkhad/all`** – Retrieves all weather alert records.
- **`GET /rsimkhad/search`** – Searches records using `PollID`, `PollTime`, and `Severity` query parameters.

The search endpoint includes input validation to reject unsupported parameters, duplicate or empty values, invalid characters, and values exceeding 100 characters.

## Usage

- Go
- Gorilla Mux
- AWS DynamoDB
- Docker
- AWS EC2
- Loggly
- Postman

## Deployment

The application is containerized using Docker and deployed on an AWS EC2 instance. Request activity is logged to Loggly, and the API endpoints can be tested using Postman.

