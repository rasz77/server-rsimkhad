FROM golang:1.27.1-alpine AS builder

WORKDIR /app

COPY go.mod ./
COPY main.go ./

RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=linux go build -o status-server main.go

FROM alpine:3.22

WORKDIR /app

COPY --from=builder /app/status-server /app/status-server

EXPOSE 8080

CMD ["/app/status-server"]