package txtstore

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// defaultOpTimeout bounds each Redis call so a slow/dead backend can't wedge a DNS response or admin request.
const defaultOpTimeout = 2 * time.Second

// keyGrace keeps a key alive slightly past its furthest member expiry, so clock
// skew or a late reader doesn't drop a still-valid record before its score lapses.
const keyGrace = time.Minute

// RedisConfig configures a RedisStore.
type RedisConfig struct {
	Addr      string // host:port
	Password  string
	DB        int
	KeyPrefix string        // prefixes every key; defaults to "opendns:txt:"
	OpTimeout time.Duration // per-call timeout; defaults to defaultOpTimeout
}

// RedisStore backs Store with a shared Redis: each FQDN is a sorted set, members
// are TXT values, scores are nanosecond expiry — giving per-value expiry (a plain
// key TTL can't) in a single-round-trip read.
type RedisStore struct {
	rdb       *redis.Client
	prefix    string
	opTimeout time.Duration
	now       func() time.Time
}

// NewRedis builds a RedisStore without connecting; call Ping to verify. The client
// reconnects on its own, so a backend briefly down at startup recovers without a restart.
func NewRedis(cfg RedisConfig) *RedisStore {
	prefix := cfg.KeyPrefix
	if prefix == "" {
		prefix = "opendns:txt:"
	}
	to := cfg.OpTimeout
	if to <= 0 {
		to = defaultOpTimeout
	}
	return &RedisStore{
		rdb: redis.NewClient(&redis.Options{
			Addr:     cfg.Addr,
			Password: cfg.Password,
			DB:       cfg.DB,
		}),
		prefix:    prefix,
		opTimeout: to,
		now:       time.Now,
	}
}

// Ping checks that the backend is reachable.
func (s *RedisStore) Ping() error {
	ctx, cancel := s.ctx()
	defer cancel()
	return s.rdb.Ping(ctx).Err()
}

// Close releases the connection pool.
func (s *RedisStore) Close() error { return s.rdb.Close() }

func (s *RedisStore) ctx() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.opTimeout)
}

func (s *RedisStore) rkey(fqdn string) string { return s.prefix + key(fqdn) }

func (s *RedisStore) Set(fqdn, value string, ttl time.Duration) error {
	ctx, cancel := s.ctx()
	defer cancel()
	k := s.rkey(fqdn)
	now := s.now()

	// Upsert the member; ZAdd overwrites the score, refreshing the expiry.
	if err := s.rdb.ZAdd(ctx, k, redis.Z{Score: float64(now.Add(ttl).UnixNano()), Member: value}).Err(); err != nil {
		return err
	}
	// Drop members that have already lapsed.
	if err := s.rdb.ZRemRangeByScore(ctx, k, "0", strconv.FormatInt(now.UnixNano(), 10)).Err(); err != nil {
		return err
	}
	// Pin the key TTL to the furthest remaining expiry (+ grace) so empty keys disappear even if Delete never runs.
	top, err := s.rdb.ZRevRangeWithScores(ctx, k, 0, 0).Result()
	if err != nil {
		return err
	}
	if len(top) == 0 {
		return s.rdb.Del(ctx, k).Err()
	}
	return s.rdb.PExpireAt(ctx, k, time.Unix(0, int64(top[0].Score)).Add(keyGrace)).Err()
}

func (s *RedisStore) Delete(fqdn, value string) error {
	ctx, cancel := s.ctx()
	defer cancel()
	k := s.rkey(fqdn)
	if err := s.rdb.ZRem(ctx, k, value).Err(); err != nil {
		return err
	}
	n, err := s.rdb.ZCard(ctx, k).Result()
	if err != nil {
		return err
	}
	if n == 0 {
		return s.rdb.Del(ctx, k).Err()
	}
	return nil
}

func (s *RedisStore) Get(fqdn string) ([]string, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	// Live members are those whose expiry score is strictly greater than now.
	return s.rdb.ZRangeByScore(ctx, s.rkey(fqdn), &redis.ZRangeBy{
		Min: "(" + strconv.FormatInt(s.now().UnixNano(), 10),
		Max: "+inf",
	}).Result()
}

func (s *RedisStore) Snapshot() (map[string][]string, error) {
	ctx, cancel := s.ctx()
	defer cancel()
	min := "(" + strconv.FormatInt(s.now().UnixNano(), 10)
	out := map[string][]string{}
	var cursor uint64
	for {
		keys, next, err := s.rdb.Scan(ctx, cursor, s.prefix+"*", 100).Result()
		if err != nil {
			return nil, err
		}
		for _, rk := range keys {
			vals, err := s.rdb.ZRangeByScore(ctx, rk, &redis.ZRangeBy{Min: min, Max: "+inf"}).Result()
			if err != nil {
				return nil, err
			}
			if len(vals) > 0 {
				out[rk[len(s.prefix):]] = vals
			}
		}
		if cursor = next; cursor == 0 {
			break
		}
	}
	return out, nil
}
