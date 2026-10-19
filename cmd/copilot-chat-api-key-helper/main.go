package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/fondoger/copilotchat/internal/apikeyhelper"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	os.Exit(apikeyhelper.Run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}
