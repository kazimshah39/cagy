package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/kazimshah39/cagy/internal/app"
	proc "github.com/kazimshah39/cagy/internal/process"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	application := app.New(proc.OSRunner{}, os.Stdout, os.Stderr)
	if err := application.Run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "cagy: %v\n", err)
		os.Exit(1)
	}
}
