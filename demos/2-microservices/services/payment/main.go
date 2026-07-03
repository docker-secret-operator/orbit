package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

var (
	db        *sql.DB
	startTime = time.Now()
)

type HealthResponse struct {
	Status    string `json:"status"`
	Service   string `json:"service"`
	Uptime    string `json:"uptime"`
	Database  string `json:"database"`
	Timestamp int64  `json:"timestamp"`
}

type ProcessPaymentRequest struct {
	UserID int     `json:"user_id"`
	Amount float64 `json:"amount"`
}

type PaymentResponse struct {
	TransactionID string  `json:"transaction_id"`
	UserID        int     `json:"user_id"`
	Amount        float64 `json:"amount"`
	Status        string  `json:"status"`
	Timestamp     int64   `json:"timestamp"`
}

type PaymentStats struct {
	Service         string  `json:"service"`
	TransactionCount int    `json:"transaction_count"`
	TotalProcessed  float64 `json:"total_processed"`
	Uptime          float64 `json:"uptime"`
	Timestamp       int64   `json:"timestamp"`
}

func init() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://dpivot:demo_password@postgres:5432/microservices?sslmode=disable"
	}

	var err error
	db, err = sql.Open("postgres", dbURL)
	if err != nil {
		log.Fatal("Failed to open database:", err)
	}

	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)

	// Wait for database to be ready
	for i := 0; i < 30; i++ {
		err = db.Ping()
		if err == nil {
			break
		}
		log.Printf("Waiting for database... (%d/30)", i+1)
		time.Sleep(1 * time.Second)
	}

	if err != nil {
		log.Fatal("Database not ready:", err)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := db.Ping(); err != nil {
		dbStatus = "error"
	}

	health := HealthResponse{
		Status:    "healthy",
		Service:   "payment-service",
		Uptime:    time.Since(startTime).String(),
		Database:  dbStatus,
		Timestamp: time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func processPaymentHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ProcessPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	if req.Amount <= 0 {
		http.Error(w, "Invalid amount", http.StatusBadRequest)
		return
	}

	// Generate transaction ID
	txnID := fmt.Sprintf("txn_%d_%d", req.UserID, time.Now().Unix())

	// Record transaction in database (simplified - no actual payment gateway)
	err := db.QueryRow(
		"INSERT INTO orders (user_id, product_id, quantity, total_price, status, created_at) VALUES ($1, $2, $3, $4, 'payment_processed', NOW()) RETURNING id",
		req.UserID, 0, 0, req.Amount,
	).Scan(new(int))

	if err != nil {
		http.Error(w, "Payment processing failed", http.StatusInternalServerError)
		return
	}

	resp := PaymentResponse{
		TransactionID: txnID,
		UserID:        req.UserID,
		Amount:        req.Amount,
		Status:        "success",
		Timestamp:     time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func refundHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		TransactionID string `json:"transaction_id"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	resp := map[string]interface{}{
		"transaction_id": req.TransactionID,
		"status":         "refunded",
		"timestamp":      time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(resp)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var count int
	var total float64

	err := db.QueryRow("SELECT COUNT(*), COALESCE(SUM(total_price), 0) FROM orders WHERE status IN ('completed', 'payment_processed')").Scan(&count, &total)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	stats := PaymentStats{
		Service:         "payment",
		TransactionCount: count,
		TotalProcessed:  total,
		Uptime:          time.Since(startTime).Seconds(),
		Timestamp:       time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	port := os.Getenv("PAYMENT_PORT")
	if port == "" {
		port = "8005"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/process", processPaymentHandler)
	http.HandleFunc("/refund", refundHandler)
	http.HandleFunc("/stats", statsHandler)

	log.Printf("Payment service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
