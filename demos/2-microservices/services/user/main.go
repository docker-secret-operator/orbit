package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"time"

	_ "github.com/lib/pq"
)

var (
	db              *sql.DB
	authServiceURL  string
	startTime       = time.Now()
	authServiceDown = false
)

type HealthResponse struct {
	Status       string `json:"status"`
	Service      string `json:"service"`
	Uptime       string `json:"uptime"`
	Database     string `json:"database"`
	AuthService  string `json:"auth_service"`
	Timestamp    int64  `json:"timestamp"`
}

type User struct {
	ID        int       `json:"id"`
	Name      string    `json:"name"`
	Email     string    `json:"email"`
	CreatedAt time.Time `json:"created_at"`
}

type UserStats struct {
	Service       string `json:"service"`
	UserCount     int    `json:"user_count"`
	AuthStatus    string `json:"auth_status"`
	Uptime        float64 `json:"uptime"`
	Timestamp     int64  `json:"timestamp"`
}

func init() {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		dbURL = "postgres://dpivot:demo_password@postgres:5432/microservices?sslmode=disable"
	}

	authServiceURL = os.Getenv("AUTH_SERVICE_URL")
	if authServiceURL == "" {
		authServiceURL = "http://auth-service:8001"
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

	// Wait for auth service
	for i := 0; i < 30; i++ {
		resp, err := http.Get(authServiceURL + "/health")
		if err == nil && resp.StatusCode == http.StatusOK {
			resp.Body.Close()
			break
		}
		if resp != nil {
			resp.Body.Close()
		}
		log.Printf("Waiting for auth service... (%d/30)", i+1)
		time.Sleep(1 * time.Second)
	}
}

func checkAuthService() string {
	resp, err := http.Get(authServiceURL + "/health")
	if err != nil || resp.StatusCode != http.StatusOK {
		authServiceDown = true
		return "down"
	}
	resp.Body.Close()
	authServiceDown = false
	return "ok"
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	dbStatus := "ok"
	if err := db.Ping(); err != nil {
		dbStatus = "error"
	}

	authStatus := checkAuthService()

	health := HealthResponse{
		Status:      "healthy",
		Service:     "user-service",
		Uptime:      time.Since(startTime).String(),
		Database:    dbStatus,
		AuthService: authStatus,
		Timestamp:   time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(health)
}

func listUsersHandler(w http.ResponseWriter, r *http.Request) {
	if authServiceDown {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"auth service unavailable"}`)
		return
	}

	rows, err := db.Query("SELECT id, name, email, created_at FROM users ORDER BY id")
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	users := []User{}
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt); err != nil {
			http.Error(w, "Scan error", http.StatusInternalServerError)
			return
		}
		users = append(users, u)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

func getUserHandler(w http.ResponseWriter, r *http.Request) {
	if authServiceDown {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":"auth service unavailable"}`)
		return
	}

	userID := r.URL.Query().Get("id")
	if userID == "" {
		http.Error(w, "Missing user id", http.StatusBadRequest)
		return
	}

	var u User
	err := db.QueryRow("SELECT id, name, email, created_at FROM users WHERE id = $1", userID).
		Scan(&u.ID, &u.Name, &u.Email, &u.CreatedAt)

	if err == sql.ErrNoRows {
		http.Error(w, "User not found", http.StatusNotFound)
		return
	} else if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(u)
}

func createUserHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var user struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	}

	if err := json.NewDecoder(r.Body).Decode(&user); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	var id int
	err := db.QueryRow(
		"INSERT INTO users (name, email, created_at) VALUES ($1, $2, NOW()) RETURNING id",
		user.Name, user.Email,
	).Scan(&id)

	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"id":%d,"name":"%s","email":"%s"}`, id, user.Name, user.Email)
}

func statsHandler(w http.ResponseWriter, r *http.Request) {
	var userCount int
	err := db.QueryRow("SELECT COUNT(*) FROM users").Scan(&userCount)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	authStatus := "ok"
	if authServiceDown {
		authStatus = "down"
	}

	stats := UserStats{
		Service:    "user",
		UserCount:  userCount,
		AuthStatus: authStatus,
		Uptime:     time.Since(startTime).Seconds(),
		Timestamp:  time.Now().Unix(),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

func main() {
	port := os.Getenv("USER_PORT")
	if port == "" {
		port = "8002"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/users", listUsersHandler)
	http.HandleFunc("/user", getUserHandler)
	http.HandleFunc("/user/create", createUserHandler)
	http.HandleFunc("/stats", statsHandler)

	log.Printf("User service starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
