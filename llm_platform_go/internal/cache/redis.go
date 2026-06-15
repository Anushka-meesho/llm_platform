package cache

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis is the production cache backend. All operations are best-effort: a
// Redis outage degrades to cache misses, never to failed predictions.
type Redis struct {
	client *redis.Client
}

// NewRedis connects to Redis and verifies it with a ping so a misconfigured
// address fails loudly at boot, not silently at call time.
func NewRedis(addr, password string, db int) (*Redis, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     addr,
		Password: password,
		DB:       db,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping %s: %w", addr, err)
	}
	return &Redis{client: client}, nil
}

func (r *Redis) Get(ctx context.Context, key string) ([]byte, bool) {
	val, err := r.client.Get(ctx, key).Bytes()
	if err == redis.Nil {
		return nil, false
	}
	if err != nil {
		log.Printf("cache: redis get failed (treating as miss): %v", err)
		return nil, false
	}
	return val, true
}

func (r *Redis) Set(ctx context.Context, key string, val []byte, ttl time.Duration) {
	if err := r.client.Set(ctx, key, val, ttl).Err(); err != nil {
		log.Printf("cache: redis set failed (entry skipped): %v", err)
	}
}
