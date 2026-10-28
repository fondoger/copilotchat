package desktop

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

func initFileLogging(dataDir string) (func(), error) {
	directory := filepath.Join(dataDir, "logs")
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, fmt.Errorf("create application log directory: %w", err)
	}
	file, err := os.OpenFile(filepath.Join(directory, "app.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, fmt.Errorf("open application log: %w", err)
	}
	previous := log.Writer()
	log.SetOutput(file)
	return func() {
		log.SetOutput(previous)
		_ = file.Close()
	}, nil
}
