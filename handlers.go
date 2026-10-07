package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"

	"github.com/gorilla/mux"
)

type LocationPayload struct {
	Type     string  `json:"type"` // "location" or "alert" or "vote"
	DriverID string  `json:"driver_id"`
	Lon      float64 `json:"lon"`
	Lat      float64 `json:"lat"`
	Heading  float64 `json:"heading,omitempty"`
	Speed    float64 `json:"speed,omitempty"`
	AlertTyp string  `json:"alert_type,omitempty"`
	AlertID  string  `json:"alert_id,omitempty"`
	Upvote   bool    `json:"upvote,omitempty"`
}

func handleClientMessage(c *Client, message []byte) {
	var payload LocationPayload
	if err := json.Unmarshal(message, &payload); err != nil {
		return
	}

	ctx := context.Background()

	switch payload.Type {
	case "location":
		updateDriverLocation(ctx, payload.DriverID, payload.Lon, payload.Lat)
	case "alert":
		query := `INSERT INTO road_alerts (user_id, alert_type, location, heading, speed_kmh) 
				  VALUES ($1, $2, ST_SetSRID(ST_MakePoint($3, $4), 4326), $5, $6) RETURNING id`
		var alertId string
		err := DB.QueryRow(ctx, query, payload.DriverID, payload.AlertTyp, payload.Lon, payload.Lat, payload.Heading, payload.Speed).Scan(&alertId)
		if err == nil {
			payload.AlertID = alertId
			respMsg, _ := json.Marshal(payload)
			broadcastToNearby(c.hub, payload.Lon, payload.Lat, respMsg)
		}
	case "vote":
		query := `INSERT INTO alert_feedback (alert_id, user_id, is_upvote) VALUES ($1, $2, $3) ON CONFLICT (alert_id, user_id) DO NOTHING`
		DB.Exec(ctx, query, payload.AlertID, payload.DriverID, payload.Upvote)
		
		var updateQuery string
		if payload.Upvote {
			updateQuery = `UPDATE road_alerts SET confirmations_count = confirmations_count + 1 WHERE id = $1`
		} else {
			updateQuery = `UPDATE road_alerts SET rejections_count = rejections_count + 1 WHERE id = $1`
		}
		DB.Exec(ctx, updateQuery, payload.AlertID)
	}
}

func handleProxyRoute(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	valhallaUrl := os.Getenv("VALHALLA_URL") + "/route"
	
	resp, err := http.Post(valhallaUrl, "application/json", bytes.NewBuffer(body))
	if err != nil {
		http.Error(w, "Routing engine error", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	
	w.Header().Set("Content-Type", "application/json")
	io.Copy(w, resp.Body)
}

type NearbyAlertResponse struct {
	ID              string  `json:"id"`
	AlertType       string  `json:"alert_type"`
	Lat             float64 `json:"lat"`
	Lon             float64 `json:"lon"`
	Heading         float64 `json:"heading,omitempty"`
	SpeedKmh        float64 `json:"speed_kmh,omitempty"`
	Confirmations   int     `json:"confirmations_count"`
	Rejections      int     `json:"rejections_count"`
	CreatedAt       string  `json:"created_at"`
	ExpiresAt       string  `json:"expires_at"`
}

func handleNearbyAlerts(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	
	lonStr := r.URL.Query().Get("lon")
	latStr := r.URL.Query().Get("lat")
	radiusStr := r.URL.Query().Get("radius_km")
	
	if lonStr == "" || latStr == "" {
		http.Error(w, "Missing lon/lat query params", http.StatusBadRequest)
		return
	}
	
	lon, err := strconv.ParseFloat(lonStr, 64)
	if err != nil {
		http.Error(w, "Invalid lon", http.StatusBadRequest)
		return
	}
	lat, err := strconv.ParseFloat(latStr, 64)
	if err != nil {
		http.Error(w, "Invalid lat", http.StatusBadRequest)
		return
	}
	
	radiusKm := 5.0
	if radiusStr != "" {
		radiusKm, _ = strconv.ParseFloat(radiusStr, 64)
	}
	
	query := `
		SELECT id, alert_type, 
		       ST_Y(location::geometry) as lat, 
		       ST_X(location::geometry) as lon,
		       heading, speed_kmh, 
		       confirmations_count, rejections_count,
		       created_at, expires_at
		FROM road_alerts
		WHERE is_active = true
		  AND expires_at > NOW()
		  AND ST_DWithin(location, ST_SetSRID(ST_MakePoint($1, $2), 4326)::geography, $3 * 1000)
		ORDER BY created_at DESC
		LIMIT 100
	`
	
	rows, err := DB.Query(ctx, query, lon, lat, radiusKm)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	
	var alerts []NearbyAlertResponse
	for rows.Next() {
		var a NearbyAlertResponse
		if err := rows.Scan(&a.ID, &a.AlertType, &a.Lat, &a.Lon, &a.Heading, &a.SpeedKmh, &a.Confirmations, &a.Rejections, &a.CreatedAt, &a.ExpiresAt); err != nil {
			continue
		}
		alerts = append(alerts, a)
	}
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"alerts": alerts,
		"count":  len(alerts),
	})
}

type VoteRequest struct {
	AlertID string `json:"alert_id"`
	Upvote  bool   `json:"upvote"`
}

func handleVoteAlert(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	
	vars := mux.Vars(r)
	alertID := vars["id"]
	if alertID == "" {
		http.Error(w, "Missing alert_id", http.StatusBadRequest)
		return
	}
	
	var req VoteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	
	driverID := r.Header.Get("X-Driver-ID")
	if driverID == "" {
		http.Error(w, "Missing X-Driver-ID header", http.StatusUnauthorized)
		return
	}
	
	// Insert vote (idempotent via ON CONFLICT)
	query := `INSERT INTO alert_feedback (alert_id, user_id, is_upvote) VALUES ($1, $2, $3) ON CONFLICT (alert_id, user_id) DO NOTHING`
	_, err := DB.Exec(ctx, query, alertID, driverID, req.Upvote)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	
	// Update counts
	var updateQuery string
	if req.Upvote {
		updateQuery = `UPDATE road_alerts SET confirmations_count = confirmations_count + 1 WHERE id = $1`
	} else {
		updateQuery = `UPDATE road_alerts SET rejections_count = rejections_count + 1 WHERE id = $1`
	}
	DB.Exec(ctx, updateQuery, alertID)
	
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
