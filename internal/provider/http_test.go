package provider

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestHTTPClientCancellation(t *testing.T) {
	for _, headers := range []bool{false, true} {
		name := "waiting for headers"
		if headers {
			name = "reading body"
		}
		t.Run(name, func(t *testing.T) {
			ready := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if headers {
					w.WriteHeader(http.StatusOK)
					w.(http.Flusher).Flush()
				}
				close(ready)
				<-r.Context().Done()
			}))
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
			client := NewHTTPClient()
			defer client.CloseIdleConnections()
			done := make(chan error, 1)
			go func() {
				response, err := client.Do(request)
				if err == nil {
					_, err = io.ReadAll(response.Body)
					response.Body.Close()
				}
				done <- err
			}()
			select {
			case <-ready:
			case <-time.After(3 * time.Second):
				cancel()
				t.Fatal("request did not reach upstream")
			}
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal("request cancellation was not preserved", err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("cancellation did not interrupt the request")
			}
		})
	}
}

func TestHTTPClientHonorsShortDeadline(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	client := NewHTTPClient()
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if response != nil {
		response.Body.Close()
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("short request deadline was not preserved", err)
	}
}
