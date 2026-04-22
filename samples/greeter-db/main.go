package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/lib/pq"
)

var db *sql.DB

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		dsn = fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=require",
			envOr("PGHOST", "orders-db"),
			envOr("PGPORT", "5432"),
			envOr("PGUSER", "orders_admin"),
			os.Getenv("PGPASSWORD"),
			envOr("PGDATABASE", "orders"),
		)
	}

	var err error
	db, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	// Wait for DB to be ready (cloud-init may still be running)
	for i := 0; i < 30; i++ {
		if err := db.Ping(); err == nil {
			break
		}
		log.Printf("Waiting for database... (%d/30)", i+1)
		time.Sleep(2 * time.Second)
	}
	if err := db.Ping(); err != nil {
		log.Fatalf("Database not reachable after 60s: %v", err)
	}
	log.Println("Connected to database")

	// Auto-create table
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS greetings (
			id SERIAL PRIMARY KEY,
			name TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT NOW()
		)
	`); err != nil {
		log.Fatalf("Failed to create table: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/greeter/greet", handleGreet)
	mux.HandleFunc("/greeter/greetings", handleGreetings)
	mux.HandleFunc("/healthz", handleHealth)

	serverPort := envOr("PORT", "9090")
	server := http.Server{
		Addr:    ":" + serverPort,
		Handler: mux,
	}

	go func() {
		log.Printf("Starting Greeter (DB-enabled) on port %s", serverPort)
		if err := server.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("HTTP ListenAndServe error: %v", err)
		}
	}()

	stopCh := make(chan os.Signal, 1)
	signal.Notify(stopCh, syscall.SIGINT, syscall.SIGTERM)
	<-stopCh

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	log.Println("Shutting down...")
	server.Shutdown(shutdownCtx)
}

// GET /greeter/greet?name=X — greets and stores in DB
func handleGreet(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "Stranger"
	}
	message := fmt.Sprintf("Hello, %s!", name)

	_, err := db.Exec("INSERT INTO greetings (name, message) VALUES ($1, $2)", name, message)
	if err != nil {
		log.Printf("DB insert error: %v", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"message": message,
		"stored":  "true",
		"db":      "orders (DBaaS on Harvester)",
	})
}

// GET /greeter/greetings — list all greetings from DB
func handleGreetings(w http.ResponseWriter, r *http.Request) {
	rows, err := db.Query("SELECT id, name, message, created_at FROM greetings ORDER BY created_at DESC LIMIT 50")
	if err != nil {
		http.Error(w, fmt.Sprintf("query error: %v", err), http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type greeting struct {
		ID        int    `json:"id"`
		Name      string `json:"name"`
		Message   string `json:"message"`
		CreatedAt string `json:"created_at"`
	}
	var greetings []greeting
	for rows.Next() {
		var g greeting
		var t time.Time
		if err := rows.Scan(&g.ID, &g.Name, &g.Message, &t); err != nil {
			continue
		}
		g.CreatedAt = t.Format(time.RFC3339)
		greetings = append(greetings, g)
	}
	if greetings == nil {
		greetings = []greeting{}
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"greetings": greetings,
		"count":     len(greetings),
		"database":  "orders (DBaaS PostgreSQL on Harvester)",
	})
}

// GET /healthz
func handleHealth(w http.ResponseWriter, r *http.Request) {
	if err := db.Ping(); err != nil {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprintf(w, `{"status":"unhealthy","error":"%v"}`, err)
		return
	}
	fmt.Fprint(w, `{"status":"healthy","database":"connected"}`)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
