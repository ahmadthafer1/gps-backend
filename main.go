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
	r.HandleFunc("/ws", func(w http.ResponseWriter, r *http.Request) {
		serveWs(hub, w, r)
	})
	r.HandleFunc("/api/v1/route", handleProxyRoute).Methods("POST")
	r.HandleFunc("/api/v1/alerts/nearby", handleNearbyAlerts).Methods("GET")
	r.HandleFunc("/api/v1/alerts/{id}/vote", handleVoteAlert).Methods("POST")

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	log.Printf("Server listening on :%s\n", port)
	if err := http.ListenAndServe(":"+port, r); err != nil {
		log.Fatal("Server error: ", err)
	}
}
