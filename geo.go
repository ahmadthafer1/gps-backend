package main

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

func updateDriverLocation(ctx context.Context, driverId string, lon, lat float64) error {
	err := Redis.GeoAdd(ctx, "driver_positions", &redis.GeoLocation{
		Name:      driverId,
		Longitude: lon,
		Latitude:  lat,
	}).Err()
	if err == nil {
		Redis.Expire(ctx, "driver_positions", 24*time.Hour)
	}
	return err
}

func getNearbyDrivers(ctx context.Context, lon, lat float64, radiusKm float64) ([]redis.GeoLocation, error) {
	return Redis.GeoSearchLocation(ctx, "driver_positions", &redis.GeoSearchLocationQuery{
		GeoSearchQuery: redis.GeoSearchQuery{
			Longitude:  lon,
			Latitude:   lat,
			Radius:     radiusKm,
			RadiusUnit: "km",
		},
		WithCoord: true,
	}).Result()
}

func broadcastToNearby(hub *Hub, lon, lat float64, msg []byte) {
	drivers, err := getNearbyDrivers(context.Background(), lon, lat, 3.0)
	if err != nil {
		return
	}
	
	driverSet := make(map[string]bool)
	for _, d := range drivers {
		driverSet[d.Name] = true
	}

	hub.mu.RLock()
	defer hub.mu.RUnlock()
	for client := range hub.clients {
		if driverSet[client.id] {
			select {
			case client.send <- msg:
			default:
				close(client.send)
				delete(hub.clients, client)
			}
		}
	}
}
