package main

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCallAutomationProtocol(t *testing.T) {
	for _, tc := range []struct {
		name, response, want, wantError string
		wait                            bool
	}{
		{"result", `{"id":1,"result":{"id":"18446744073709551615"}}`, `{"id":"18446744073709551615"}`, "", false},
		{"remote-error", `{"id":1,"error":{"code":"no_element","message":"Missing button"}}`, "", "no_element: Missing button", false},
		{"wrong-id", `{"id":2,"result":{}}`, "", "ID does not match", false},
		{"missing-result", `{"id":1}`, "", "no result", false},
		{"timeout", "", "", "timeout", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, err := os.MkdirTemp("", "cc-auto-")
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(dir)
			socket := filepath.Join(dir, "app.sock")
			listener, err := net.Listen("unix", socket)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			if err := listener.(*net.UnixListener).SetDeadline(time.Now().Add(5 * time.Second)); err != nil {
				t.Fatal(err)
			}
			done, release := make(chan error, 1), make(chan struct{})
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- err
					return
				}
				defer conn.Close()
				var request struct {
					ID     int             `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err = json.NewDecoder(conn).Decode(&request); err == nil && (request.ID != 1 || request.Method != "act" || string(request.Params) != `{"key":"message","action":"setValue","text":"probe"}`) {
					err = errors.New("request changed method or params")
				}
				if err == nil && tc.wait {
					<-release
				} else if err == nil {
					_, err = conn.Write([]byte(tc.response + "\n"))
				}
				done <- err
			}()
			result, callErr := call(socket, "act", json.RawMessage(`{"key":"message","action":"setValue","text":"probe"}`), time.Second)
			close(release)
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			if tc.wantError != "" {
				if callErr == nil || !strings.Contains(callErr.Error(), tc.wantError) {
					t.Fatalf("error = %v, want %q", callErr, tc.wantError)
				}
			} else if callErr != nil || string(result) != tc.want {
				t.Fatalf("result = %s, error = %v", result, callErr)
			}
		})
	}
}
