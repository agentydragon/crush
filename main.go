package main

import (
	"log/slog"

	_ "github.com/joho/godotenv/autoload" // automatically load .env files

	"github.com/charmbracelet/crush/internal/cmd"
	"github.com/charmbracelet/crush/internal/log"
)

func main() {
	defer log.RecoverPanic("main", func() {
		slog.Error("Application terminated due to unhandled panic")
	})

	// pprof now started in internal/cmd when debug mode is enabled (or CRUSH_PROFILE set)

	cmd.Execute()
}
