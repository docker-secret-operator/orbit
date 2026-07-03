package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"time"

	_ "github.com/lib/pq"
)

var (
	db                *sql.DB
	userServiceURL    string
	productServiceURL string
	paymentServiceURL string
	startTime         = time.Now()

	downServices = map[string]bool{
		"user":    false,
		"product": false,
		"payment": false,
	}
)

type HealthResponse struct {
	Status              string      `json:"status"`
	Service             string      `json:"service"`
	Uptime              string      `json:"uptime"`
	Database            string      `json:"database"`
	DependentServices   map[string]string `json:"dependent_services"`
	Timestamp           int64       `json:"timestamp"`
}

type Order struct {
	ID        int       `json:"id"`
	UserID    int       `json:"user_id"`
	ProductID int       `json:"product_id"`
	Quantity  int       `json:"quantity"`
	TotalPrice float64  `json:"total_price"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type CreateOrderRequest struct {
	UserID    int `json:"user_id"`
	ProductID int `json:"product_id"`
	Quantity  int `json:"quantity"`
}

type OrderStats struct {
	Service              string      `json:"service"`
	OrderCount           int         `json:"order_count"`
	TotalRevenue         float64     `json:"total_revenue"`
	DependentServices    map[string]string `json:"dependent_services"`
	Uptime               float64     `json:"uptime"`
	Timestamp            int64       `json:"timestamp"`
}

func init() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://dpivot:demo_password@postgres:5432/microservices?sslmode=disable"
	}

	userServiceURL = os.Getenv("USER_SERVICE_URL")
	if userServiceURL == "" {
		userServiceURL = "http://user-service:8002"
	}

	productServiceURL = os.Getenv("PRODUCT_SERVICE_URL")
	if productServiceURL == "" {
		productServiceURL = "http://product-service:8003"
	}

	paymentServiceURL = os.Getenv("PAYMENT_SERVICE_URL")
	if paymentServiceURL == "" {
		paymentServiceURL = "http://payment-service:8005"
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

	// Check all dependent services
	waitForDependencies()
}

func waitForDependencies() {
	for _, svc := range []struct {
		name string
		url  string
	}{
		{"user", userServiceURL},
		{"product", productServiceURL},
		{"payment", paymentServiceURL},
	} {
		for i := 0; i < 30; i++ {
			resp, err := http.Get(svc.url + "/health")
			if err == nil && resp.StatusCode == http.StatusOK {
				resp.Body.Close()
				downServices[svc.name] = false
				break
			}
			if resp != nil {
				resp.Body.Close()
			}
			log.Printf("Waiting for %s service... (%d/30)", svc.name, i+1)
			time.Sleep(1 * time.Second)
		}
	}
}

func checkService(serviceName, serviceURL string) bool {
	resp, err := http.Get(serviceURL + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		downServices[serviceName] = true
		return false
	}
	resp.Body.Close()
	downServices[serviceName] = false
	return true
}

func getServiceStatus() map[string]string {
	status := make(map[string]string)
	checkService("user", userServiceURL)
	checkService("product", productServiceURL)
	checkService("payment", paymentServiceURL)

	for svc, down := range downServices {
		if down {
			status[svc] = "down"
		} else {
			status[svc] = "ok"
		}
	}
	return status
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := db.Ping(); err != nil {
		dbStatus = "error"
	}

	depStatus := getServiceStatus()

	health := HealthResponse{
		Status:            "healthy",
		Service:           "order-service",
		Uptime:            time.Since(startTime).String(),
		Database:          dbStatus,
		DependentServices: depStatus,
		Timestamp:         time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func listOrdersHandler(w http.ResponseWriter, r *http.Request) {
	depStatus := getServiceStatus()
	if !depStatus["user"] || !depStatus["product"] || !depStatus["payment"] {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"dependent service unavailable","services":%v}`, depStatus)
		return
	}

	rows, err := db.Query("SELECT id, user_id, product_id, quantity, total_price, status, created_at FROM orders ORDER BY id")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	orders := []Order{}
	for rows.Next() {
		var o Order
		if err := rows.Scan(&o.ID, &o.UserID, &o.ProductID, &o.Quantity, &o.TotalPrice, &o.Status, &o.CreatedAt); err != nil {
			http.Error(w, "Scan error", http.StatusInternalServerError)
			return
		}
		orders = append(orders, o)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(orders)
}

func createOrderHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req CreateOrderRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	// Check dependencies
	depStatus := getServiceStatus()
	if !depStatus["user"] || !depStatus["product"] || !depStatus["payment"] {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"error":"dependent service unavailable","services":%v}`, depStatus)
		return
	}

	// Verify user exists
	userResp, err := http.Get(fmt.Sprintf("%s/user?id=%d", userServiceURL, req.UserID))
	if err != nil || userResp.StatusCode != http.StatusOK {
		if userResp != nil {
			userResp.Body.Close()
		}
		http.Error(w, "User not found", http.StatusBadRequest)
		return
	}
	userResp.Body.Close()

	// Verify product exists and get price
	productResp, err := http.Get(fmt.Sprintf("%s/product?id=%d", productServiceURL, req.ProductID))
	if err != nil || productResp.StatusCode != http.StatusOK {
		if productResp != nil {
			productResp.Body.Close()
		}
		http.Error(w, "Product not found", http.StatusBadRequest)
		return
	}

	var product struct {
		Price float64 `json:"price"`
	}
	json.NewDecoder(productResp.Body).Decode(&product)
	productResp.Body.Close()

	totalPrice := product.Price * float64(req.Quantity)

	// Process payment
	paymentReq := map[string]interface{}{
		"user_id": req.UserID,
		"amount":  totalPrice,
	}
	paymentBody, _ := json.Marshal(paymentReq)
	paymentResp, err := http.Post(paymentServiceURL+"/process", "application/json", bytes.NewBuffer(paymentBody))
	if err != nil || paymentResp.StatusCode != http.StatusOK {
		if paymentResp != nil {
			paymentResp.Body.Close()
		}
		http.Error(w, "Payment failed", http.StatusPaymentRequired)
		return
	}
	paymentResp.Body.Close()

	// Create order
	var orderID int
	err = db.QueryRow(
		"INSERT INTO orders (user_id, product_id, quantity, total_price, status, created_at) VALUES ($1, $2, $3, $4, 'completed', NOW()) RETURNING id",
		req.UserID, req.ProductID, req.Quantity, totalPrice,
	).Scan(&orderID)

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%d,"user_id":%d,"product_id":%d,"quantity":%d,"total_price":%.2f,"status":"completed"}`,
		orderID, req.UserID, req.ProductID, req.Quantity, totalPrice)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var count int
	var revenue float64

	err := db.QueryRow("SELECT COUNT(*), COALESCE(SUM(total_price), 0) FROM orders WHERE status = 'completed'").Scan(&count, &revenue)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	depStatus := getServiceStatus()

	stats := OrderStats{
		Service:            "order",
		OrderCount:         count,
		TotalRevenue:       revenue,
		DependentServices:  depStatus,
		Uptime:             time.Since(startTime).Seconds(),
		Timestamp:          time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	port := os.Getenv("ORDER_PORT")
	if port == "" {
		port = "8004"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/orders", listOrdersHandler)
	http.HandleFunc("/order/create", createOrderHandler)
	http.HandleFunc("/stats", statsHandler)

	log.Printf("Order service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
