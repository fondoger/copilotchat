package desktop

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileLoggingAppendsAcrossStarts(t *testing.T) {
	var terminal bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&terminal)
	t.Cleanup(func() { log.SetOutput(previous) })
	logger := log.Default()
	dataDir := filepath.Join(t.TempDir(), "custom-data")
	for _, message := range []string{"first-start", "second-start"} {
		func() {
			closeLog, err := initFileLogging(dataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer closeLog()
			logger.Print(message)
		}()
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "logs", "app.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range []string{"first-start", "second-start"} {
		if strings.Count(string(data), message) != 1 {
			t.Errorf("log did not retain %q across starts: %s", message, data)
		}
	}
	if terminal.Len() != 0 {
		t.Fatal("application logs still depended on terminal output", terminal.String())
	}
	log.Print("restored-output")
	if !strings.Contains(terminal.String(), "restored-output") {
		t.Fatal("logger output was not restored before closing the file")
	}
}

func TestFileLoggingFailurePreservesLogger(t *testing.T) {
	var output bytes.Buffer
	previous := log.Writer()
	log.SetOutput(&output)
	t.Cleanup(func() { log.SetOutput(previous) })
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "logs", "app.log"), 0700); err != nil {
		t.Fatal(err)
	}
	closeLog, err := initFileLogging(dataDir)
	if err == nil {
		closeLog()
		t.Fatal("log initialization succeeded with a directory in place of app.log")
	}
	if !strings.Contains(err.Error(), "open application log") {
		t.Fatal("missing logging failure context", err)
	}
	log.Print("initialization-failed")
	if !strings.Contains(output.String(), "initialization-failed") {
		t.Fatal("failed initialization replaced the previous logger")
	}
}
