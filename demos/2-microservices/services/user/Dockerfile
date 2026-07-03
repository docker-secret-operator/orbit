FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -o user main.go

FROM alpine:latest
RUN apk --no-cache add curl ca-certificates postgresql-client
COPY --from=builder /app/user /usr/local/bin/

EXPOSE 8002
CMD ["user"]
