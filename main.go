package main

import (
	"flag"
	"log"

	"github.com/fondoger/copilotchat/internal/desktop"
)

func main() {
	dataDir := flag.String("data-dir", "", "Override the local conversation directory")
	flag.Parse()
	if err := desktop.Run(*dataDir); err != nil {
		log.Fatal(err)
	}
}
