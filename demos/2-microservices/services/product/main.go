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

type Product struct {
	ID          int       `json:"id"`
	Name        string    `json:"name"`
	Price       float64   `json:"price"`
	Description string    `json:"description"`
	CreatedAt   time.Time `json:"created_at"`
}

type ProductStats struct {
	Service      string  `json:"service"`
	ProductCount int     `json:"product_count"`
	AveragePrice float64 `json:"average_price"`
	Uptime       float64 `json:"uptime"`
	Timestamp    int64   `json:"timestamp"`
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
		Service:   "product-service",
		Uptime:    time.Since(startTime).String(),
		Database:  dbStatus,
		Timestamp: time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func listProductsHandler(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, price, description, created_at FROM products ORDER BY id")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	products := []Product{}
	for rows.Next() {
		var p Product
		if err := rows.Scan(&p.ID, &p.Name, &p.Price, &p.Description, &p.CreatedAt); err != nil {
			http.Error(w, "Scan error", http.StatusInternalServerError)
			return
		}
		products = append(products, p)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(products)
}

func getProductHandler(w http.ResponseWriter, r *http.Request) {
	productID := r.URL.Query().Get("id")
	if productID == "" {
		http.Error(w, "Missing product id", http.StatusBadRequest)
		return
	}

	var p Product
	err := db.QueryRow("SELECT id, name, price, description, created_at FROM products WHERE id = $1", productID).
		Scan(&p.ID, &p.Name, &p.Price, &p.Description, &p.CreatedAt)

	if err == sql.ErrNoRows {
		http.Error(w, "Product not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(p)
}

func createProductHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var product struct {
		Name        string  `json:"name"`
		Price       float64 `json:"price"`
		Description string  `json:"description"`
	}

	if err := json.NewDecoder(r.Body).Decode(&product); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var id int
	err := db.QueryRow(
		"INSERT INTO products (name, price, description, created_at) VALUES ($1, $2, $3, NOW()) RETURNING id",
		product.Name, product.Price, product.Description,
	).Scan(&id)

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%d,"name":"%s","price":%.2f,"description":"%s"}`, id, product.Name, product.Price, product.Description)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var count int
	var avgPrice float64

	err := db.QueryRow("SELECT COUNT(*), COALESCE(AVG(price), 0) FROM products").Scan(&count, &avgPrice)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	stats := ProductStats{
		Service:      "product",
		ProductCount: count,
		AveragePrice: avgPrice,
		Uptime:       time.Since(startTime).Seconds(),
		Timestamp:    time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	port := os.Getenv("PRODUCT_PORT")
	if port == "" {
		port = "8003"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/products", listProductsHandler)
	http.HandleFunc("/product", getProductHandler)
	http.HandleFunc("/product/create", createProductHandler)
	http.HandleFunc("/stats", statsHandler)

	log.Printf("Product service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
