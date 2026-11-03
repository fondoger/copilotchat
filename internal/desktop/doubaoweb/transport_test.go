package doubaoweb

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fondoger/copilotchat/internal/provider"
	"github.com/fondoger/copilotchat/internal/provider/doubao"
)

func assertUnavailable(t *testing.T, err error) {
	t.Helper()
	var detail *provider.HTTPError
	if !errors.As(err, &detail) || detail.StatusCode != http.StatusBadGateway || err.Error() != doubao.SessionUnavailable().Error() {
		t.Fatalf("expected safe session failure, got %v", err)
	}
}

func TestReadinessTimeoutDoesNotBecomeRequestDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		err := waitReady(context.Background(), context.Background(), func(context.Context) (bool, error) {
			return false, errors.New("page failure with private state")
		})
		assertUnavailable(t, err)
		if time.Since(start) != 40*time.Second {
			t.Fatal("readiness did not use its own startup deadline")
		}
	})
	for _, reason := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(reason.Error(), func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if reason == context.Canceled {
					cancel()
				} else {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
					defer deadlineCancel()
				}
				err := waitReady(ctx, ctx, func(context.Context) (bool, error) { return false, nil })
				if !errors.Is(err, reason) {
					t.Fatalf("caller cancellation lost priority: %v", err)
				}
			})
		})
	}
}

func TestBrowserBodyEOFClosesBeforeCancel(t *testing.T) {
	// Exercise the cancellation watcher race repeatedly, including bytes returned
	// with done=true. A successful body must never end in context.Canceled.
	for range 100 {
		body := newBrowserBody(context.Background(), func(context.Context) (readResult, error) {
			return readResult{Done: true, Data: base64.StdEncoding.EncodeToString([]byte("final bytes"))}, nil
		})
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(data) != "final bytes" {
			t.Fatalf("normal EOF corrupted by teardown: %q, %v", data, err)
		}
	}
}

func TestBrowserBodySanitizesPageAndEncodingFailures(t *testing.T) {
	for _, broken := range []bool{false, true} {
		body := newBrowserBody(context.Background(), func(context.Context) (readResult, error) {
			if broken {
				return readResult{Data: "invalid base64!"}, nil
			}
			return readResult{}, errors.New("private signed URL or cookie")
		})
		_, err := io.ReadAll(body)
		body.Close()
		assertUnavailable(t, err)
		if strings.Contains(err.Error(), "private") {
			t.Fatal("browser failure leaked page data")
		}
	}
}

func TestBrowserBodyCancelAndCloseUnblockPendingRead(t *testing.T) {
	for _, closeBody := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel session", true: "close body"}[closeBody], func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				stopped := make(chan struct{})
				body := newBrowserBody(ctx, func(ctx context.Context) (readResult, error) {
					<-ctx.Done()
					close(stopped)
					return readResult{}, errors.New("private browser error after cancellation")
				})
				defer body.Close()
				if closeBody {
					body.Close()
					body.Close()
				} else {
					cancel()
				}
				_, err := io.ReadAll(body)
				want := context.Canceled
				if closeBody {
					want = io.ErrClosedPipe
				}
				if !errors.Is(err, want) {
					t.Fatalf("pending read returned %v, want %v", err, want)
				}
				<-stopped
			})
		})
	}
}
