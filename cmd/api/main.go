package main

import (
	"log"
	"net/http"

	"github.com/Joedee43/interior_api.git/internal/config"
	"github.com/Joedee43/interior_api.git/internal/database"
	"github.com/Joedee43/interior_api.git/internal/router"
	
)

func main() {
	cfg := config.Load()

	db, err := database.Connect(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database connection failed: %v", err)
	}
	if db != nil {
		defer db.Close()
	}

	r := router.Setup(db)

	addr := ":" + cfg.Port
	if err := http.ListenAndServe(addr, r); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
