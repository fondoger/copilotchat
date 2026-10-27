package main

import (
	"flag"
	"log"

	"github.com/egoist/mygo"
	"github.com/fondoger/copilotchat/internal/desktop"
)

func main() {
	dataDir := flag.String("data-dir", "", "Override the local conversation directory")
	dev := flag.Bool("dev", mygo.IsDev(), "Run an isolated development instance (default: automatic build-mode detection)")
	flag.Parse()
	if err := desktop.Run(*dataDir, *dev); err != nil {
		log.Fatal(err)
	}
}
