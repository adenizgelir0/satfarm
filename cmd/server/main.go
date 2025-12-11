package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"

	satgrpc "satfarm/internal/grpc"
	"satfarm/internal/web"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func main() {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("DATABASE_URL is not set")
	}

	db, err := initDB(dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	tpl, err := web.LoadTemplates("templates/*.html")
	if err != nil {
		log.Fatal(err)
	}

	// --- gRPC worker server state: registry + scheduler inside ---
	grpcSrv := satgrpc.NewGRPCServer(db)

	// --- Web server: give it the scheduler so /jobs/new can enqueue work ---
	wsrv := web.NewServer(db, tpl, grpcSrv.Scheduler())
	r := web.NewRouter(wsrv)

	// --- Start gRPC worker listener in a goroutine ---
	go func() {
		addr := ":50051"
		log.Printf("gRPC worker server listening on %s", addr)
		if err := grpcSrv.Serve(addr); err != nil {
			log.Fatalf("gRPC server error: %v", err)
		}
	}()

	// --- Start HTTP dashboard / API ---
	log.Println("HTTP server listening on :8080")
	if err := http.ListenAndServe(":8080", r); err != nil {
		log.Fatal(err)
	}
}

func initDB(dsn string) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		return nil, err
	}

	schemaBytes, err := os.ReadFile("/app/db/schema.sql")
	if err != nil {
		return nil, fmt.Errorf("schema read error: %w", err)
	}
	if _, err := db.Exec(string(schemaBytes)); err != nil {
		return nil, fmt.Errorf("schema exec error: %w", err)
	}

	return db, nil
}
