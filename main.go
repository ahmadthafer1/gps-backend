package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/gorilla/mux"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

var (
	DB    *pgxpool.Pool
	Redis *redis.Client
)

func main() {
	ctx := context.Background()

	dbUrl := os.Getenv("DB_URL")
	var err error
	DB, err = pgxpool.New(ctx, dbUrl)
	if err != nil {
		log.Fatalf("Unable to connect to database: %v\n", err)
	}
	defer DB.Close()

	// Support both REDIS_URL (full URL with TLS for Upstash) and REDIS_ADDR (plain host:port)
	redisUrl := os.Getenv("REDIS_URL")
	if redisUrl != "" {
		opt, err := redis.ParseURL(redisUrl)
		if err != nil {
			log.Fatalf("Invalid REDIS_URL: %v\n", err)
		}
		Redis = redis.NewClient(opt)
	} else {
		redisAddr := os.Getenv("REDIS_ADDR")
		Redis = redis.NewClient(&redis.Options{Addr: redisAddr})
	}

	hub := newHub()
	go hub.run()

	r := mux.NewRouter()
	
	// API Endpoints
	r.HandleFunc("/api/v1/route", handleProxyRoute).Methods("POST")
	r.HandleFunc("/api/v1/alerts/nearby", handleNearbyAlerts).Methods("GET")
	r.HandleFunc("/api/v1/alerts/{id}/vote", handleVoteAlert).Methods("POST")
	
	// WebSocket server
	go func() {
		wsMux := http.NewServeMux()
		wsMux.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
			serveWs(hub, w, r)
		})
		log.Println("WebSocket server listening on :8081")
		if err := http.ListenAndServe(":8081", wsMux); err != nil {
			log.Fatal("WebSocket server error: ", err)
		}
	}()

	log.Println("HTTP server listening on :8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal("HTTP server error: ", err)
	}
}
