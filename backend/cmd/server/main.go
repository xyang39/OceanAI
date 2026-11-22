package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata"

	"github.com/jackc/pgx/v5/pgxpool"
	"oceanai/backend/internal/api"
	"oceanai/backend/internal/store"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		client := http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/api/health")
		if err != nil {
			os.Exit(1)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := run(port); err != nil {
		log.Fatal(err)
	}
}

func run(port string) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	startup, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return err
	}
	config.MaxConns = 10
	pool, err := pgxpool.NewWithConfig(startup, config)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err = pool.Ping(startup); err != nil {
		return err
	}
	if err = store.Bootstrap(startup, pool); err != nil {
		return err
	}
	if len(os.Args) > 1 && os.Args[1] == "seed-large" {
		flags := flag.NewFlagSet("seed-large", flag.ContinueOnError)
		tenant := flags.String("tenant", "east", "tenant ID")
		count := flags.Int("count", 100000, "number of fixture certificates")
		if err := flags.Parse(os.Args[2:]); err != nil {
			return err
		}
		if err := store.GenerateLarge(ctx, pool, *tenant, *count); err != nil {
			return err
		}
		log.Printf("large fixture ready: tenant=%s count=%d", *tenant, *count)
		return nil
	}
	server := &http.Server{
		Addr: ":" + port, Handler: api.Router(pool),
		ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 90 * time.Second, IdleTimeout: 60 * time.Second,
	}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	log.Printf("Gin API ready on :%s; PostgreSQL schema and demo data ready", port)
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
