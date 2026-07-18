package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/miekg/dns"
	"golang.org/x/sync/errgroup"

	"github.com/viaveritas/opendns/internal/adminsrv"
	"github.com/viaveritas/opendns/internal/config"
	"github.com/viaveritas/opendns/internal/dnssrv"
	"github.com/viaveritas/opendns/internal/txtstore"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(log)

	cfg, err := config.Load()
	if err != nil {
		log.Error("config load failed", "err", err)
		os.Exit(2)
	}
	log.Info("starting opendns",
		"zone", cfg.Zone,
		"ns", cfg.NS,
		"dns_bind", cfg.DNSBind,
		"admin_bind", cfg.AdminBind,
		"admin_auth", cfg.AdminToken != "",
		"txt_store", storeKind(cfg.RedisAddr),
	)
	if cfg.AdminToken == "" {
		log.Warn("OPENDNS_ADMIN_TOKEN is unset — admin API is unauthenticated; keep it on a private interface")
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// TXT store: Redis (shared across instances) when configured, else in-memory.
	var store txtstore.Store
	var closeStore func()
	if cfg.RedisAddr != "" {
		rs := txtstore.NewRedis(txtstore.RedisConfig{
			Addr:      cfg.RedisAddr,
			Password:  cfg.RedisPassword,
			DB:        cfg.RedisDB,
			KeyPrefix: cfg.RedisKeyPrefix,
		})
		if err := rs.Ping(); err != nil {
			// Non-fatal: keep serving A/AAAA/SOA; TXT recovers when Redis reconnects.
			log.Warn("redis unreachable at startup; TXT records unavailable until it recovers", "addr", cfg.RedisAddr, "err", err)
		} else {
			log.Info("txt store: redis", "addr", cfg.RedisAddr)
		}
		store = rs
		closeStore = func() { _ = rs.Close() }
	} else {
		ms := txtstore.NewMem()
		stopGC := make(chan struct{})
		go ms.RunGC(stopGC, 30*time.Second)
		closeStore = func() { close(stopGC) }
		store = ms
		log.Info("txt store: in-memory (single instance; set OPENDNS_REDIS_ADDR to share across instances)")
	}
	defer closeStore()

	handler := dnssrv.New(cfg, store)

	udp := &dns.Server{Addr: cfg.DNSBind, Net: "udp", Handler: handler}
	tcp := &dns.Server{Addr: cfg.DNSBind, Net: "tcp", Handler: handler}
	admin := &http.Server{
		Addr:              cfg.AdminBind,
		Handler:           adminsrv.Handler(store, cfg.AdminToken),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	g, gctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		log.Info("dns udp listening", "addr", cfg.DNSBind)
		if err := udp.ListenAndServe(); err != nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		log.Info("dns tcp listening", "addr", cfg.DNSBind)
		if err := tcp.ListenAndServe(); err != nil {
			return err
		}
		return nil
	})
	g.Go(func() error {
		log.Info("admin http listening", "addr", cfg.AdminBind)
		if err := admin.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	})

	// Shut down on either a signal or any listener failure.
	g.Go(func() error {
		<-gctx.Done()
		shutCtx, c := context.WithTimeout(context.Background(), 5*time.Second)
		defer c()
		// Stop admin first for a clean client shutdown, then DNS listeners.
		_ = admin.Shutdown(shutCtx)
		_ = udp.ShutdownContext(shutCtx)
		_ = tcp.ShutdownContext(shutCtx)
		return nil
	})

	if err := g.Wait(); err != nil && !errors.Is(err, context.Canceled) {
		log.Error("server exited with error", "err", err)
		os.Exit(1)
	}
	log.Info("shutdown complete")
}

func storeKind(redisAddr string) string {
	if redisAddr != "" {
		return "redis"
	}
	return "memory"
}
