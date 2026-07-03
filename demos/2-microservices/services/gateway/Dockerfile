FROM golang:1.21-alpine AS builder

WORKDIR /app
COPY go.* ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o gateway main.go

FROM alpine:latest
RUN apk --no-cache add curl ca-certificates
COPY --from=builder /app/gateway /usr/local/bin/

EXPOSE 3000
CMD ["gateway"]
