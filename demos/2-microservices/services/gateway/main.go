package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"
)

type ServiceRoute struct {
	Name   string
	URL    string
	Prefix string
}

var (
	routes []ServiceRoute
	client *http.Client
)

func init() {
	client = &http.Client{
		Timeout: 10 * time.Second,
	}

	routes = []ServiceRoute{
		{Name: "auth", URL: os.Getenv("AUTH_SERVICE_URL"), Prefix: "/auth"},
		{Name: "user", URL: os.Getenv("USER_SERVICE_URL"), Prefix: "/users"},
		{Name: "product", URL: os.Getenv("PRODUCT_SERVICE_URL"), Prefix: "/products"},
		{Name: "order", URL: os.Getenv("ORDER_SERVICE_URL"), Prefix: "/orders"},
		{Name: "payment", URL: os.Getenv("PAYMENT_SERVICE_URL"), Prefix: "/payments"},
	}
}

func proxyHandler(route ServiceRoute) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		upstreamURL, err := url.Parse(route.URL)
		if err != nil {
			http.Error(w, "Invalid upstream URL", http.StatusInternalServerError)
			return
		}

		proxy := httputil.NewSingleHostReverseProxy(upstreamURL)
		proxy.Transport = &http.Transport{
			MaxIdleConns:       100,
			IdleConnTimeout:    90 * time.Second,
			DisableCompression: true,
		}

		// Rewrite path
		r.URL.Scheme = upstreamURL.Scheme
		r.URL.Host = upstreamURL.Host
		r.RequestURI = ""

		// Add service tracing header
		r.Header.Set("X-Forwarded-For", r.RemoteAddr)
		r.Header.Set("X-Service-Route", route.Name)

		proxy.ServeHTTP(w, r)
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	status := map[string]interface{}{
		"status":    "healthy",
		"service":   "gateway",
		"timestamp": time.Now().Unix(),
		"uptime":    time.Since(startTime).Seconds(),
		"routes":    len(routes),
	}

	// Check all upstream services
	services := make(map[string]string)
	for _, route := range routes {
		resp, err := client.Get(route.URL + "/health")
		if err != nil || resp.StatusCode != http.StatusOK {
			services[route.Name] = "unhealthy"
		} else {
			services[route.Name] = "healthy"
			resp.Body.Close()
		}
	}
	status["services"] = services

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"status":"%s","service":"gateway","timestamp":%d,"uptime":%.2f,"services":%v}`,
		status["status"], status["timestamp"], status["uptime"], services)
}

func rootHandler(w http.ResponseWriter, r *http.Request) {
	routes := make(map[string]string)
	routes["/health"] = "Gateway health check"
	routes["/auth/*"] = "Authentication service"
	routes["/users/*"] = "User service"
	routes["/products/*"] = "Product service"
	routes["/orders/*"] = "Order service"
	routes["/payments/*"] = "Payment service"

	w.Header().Set("Content-Type", "application/json")
	fmt.Fprint(w, `{"service":"gateway","routes":{`)
	first := true
	for path, desc := range routes {
		if !first {
			fmt.Fprint(w, ",")
		}
		fmt.Fprintf(w, `"%s":"%s"`, path, desc)
		first = false
	}
	fmt.Fprint(w, "}}")
}

var startTime = time.Now()

func main() {
	port := os.Getenv("GATEWAY_PORT")
	if port == "" {
		port = "3000"
	}

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/", rootHandler)

	for _, route := range routes {
		prefix := route.Prefix
		http.HandleFunc(prefix+"/", func(route ServiceRoute) http.HandlerFunc {
			return proxyHandler(route)
		}(route))
	}

	log.Printf("Gateway starting on port %s", port)
	log.Fatal(http.ListenAndServe(":"+port, nil))
}
