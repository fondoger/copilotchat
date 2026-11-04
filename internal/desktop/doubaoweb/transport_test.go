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

	"github.com/fondoger/copilotchat/internal/desktop/webtransport"
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

func TestChatAndTitleRequestsUseBrowserTransport(t *testing.T) {
	for _, path := range []string{"/chat/completion", "/im/conversation/info"} {
		t.Run(path, func(t *testing.T) {
			const contentType = "application/json; encoding=utf-8"
			body := []byte(`{"conversation_id":"conversation"}`)
			signedURL := "https://www.doubao.com" + path + "?a_bogus=signed"
			fetched := false
			transport := &Transport{ctx: context.Background(), call: func(_ context.Context, operation string, input any) (any, error) {
				switch operation {
				case "sign":
					args := input.(signInput)
					if args.Path != path || args.Tab == "" {
						t.Fatalf("unexpected signing input: %+v", args)
					}
					return signResult{URL: signedURL, CSRF: "csrf"}, nil
				case "fetch":
					args := input.(fetchInput)
					if args.URL != signedURL || args.ContentType != contentType || args.CSRF != "csrf" || args.Body != base64.StdEncoding.EncodeToString(body) {
						t.Fatalf("browser fetch lost signed URL, headers or body: %+v", args)
					}
					fetched = true
					return fetchResult{Status: http.StatusOK, ContentType: "application/json"}, nil
				case "read":
					return webtransport.Chunk{Done: true, Data: base64.StdEncoding.EncodeToString([]byte(`{"name":"reused title"}`))}, nil
				default:
					return nil, errors.New("unexpected operation")
				}
			}}
			resp, err := transport.Do(context.Background(), path, contentType, body)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			data, err := io.ReadAll(resp.Body)
			if !fetched || err != nil || string(data) != `{"name":"reused title"}` || resp.Header.Get("Content-Type") != "application/json" {
				t.Fatalf("browser response: fetched=%v, data=%q, headers=%v, error=%v", fetched, data, resp.Header, err)
			}
		})
	}
}

func TestTitleRequestRejectsUntrustedPathsAndDestinations(t *testing.T) {
	for _, path := range []string{"/im/conversation/info/", "/im/conversation/info?extra=1", "/im/conversation/list", "/im/conversation/%69nfo"} {
		transport := &Transport{ctx: context.Background()}
		_, err := transport.Do(context.Background(), path, "application/json", nil)
		assertUnavailable(t, err)
	}
	for _, destination := range []string{
		"http://www.doubao.com/im/conversation/info",
		"https://attacker.invalid/im/conversation/info",
		"https://www.doubao.com:443/im/conversation/info",
		"https://user@www.doubao.com/im/conversation/info",
		"https://www.doubao.com/im/conversation/list",
	} {
		t.Run(destination, func(t *testing.T) {
			transport := &Transport{ctx: context.Background(), call: func(_ context.Context, operation string, _ any) (any, error) {
				if operation != "sign" {
					t.Fatalf("untrusted signed URL reached %s", operation)
				}
				return signResult{URL: destination}, nil
			}}
			_, err := transport.Do(context.Background(), "/im/conversation/info", "application/json", nil)
			assertUnavailable(t, err)
		})
	}
}

func TestDoCancellationCoversBrowserBody(t *testing.T) {
	for _, action := range []string{"request canceled", "request deadline", "session canceled", "close body"} {
		t.Run(action, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				session, cancelSession := context.WithCancel(context.Background())
				defer cancelSession()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if action == "request deadline" {
					var deadlineCancel context.CancelFunc
					ctx, deadlineCancel = context.WithTimeout(ctx, time.Second)
					defer deadlineCancel()
				}
				started, stopped := make(chan struct{}), make(chan struct{})
				pending := true
				transport := &Transport{ctx: session, call: func(ctx context.Context, operation string, _ any) (any, error) {
					switch operation {
					case "sign":
						return signResult{URL: "https://www.doubao.com/im/conversation/info?a_bogus=signed"}, nil
					case "fetch":
						return fetchResult{Status: http.StatusOK, ContentType: "application/json"}, nil
					case "read":
						if !pending {
							return webtransport.Chunk{Done: true}, nil
						}
						close(started)
						<-ctx.Done()
						close(stopped)
						return nil, errors.New("private browser error after cancellation")
					default:
						return nil, errors.New("unexpected operation")
					}
				}}
				resp, err := transport.Do(ctx, "/im/conversation/info", "application/json; encoding=utf-8", nil)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				<-started
				want := context.Canceled
				switch action {
				case "request canceled":
					cancel()
				case "request deadline":
					want = context.DeadlineExceeded
				case "session canceled":
					cancelSession()
				case "close body":
					resp.Body.Close()
					resp.Body.Close()
					want = io.ErrClosedPipe
				}
				_, err = io.ReadAll(resp.Body)
				resp.Body.Close()
				if !errors.Is(err, want) {
					t.Fatalf("body read returned %v, want %v", err, want)
				}
				<-stopped
				if action != "session canceled" {
					if session.Err() != nil {
						t.Fatal("request cancellation also canceled the session")
					}
					pending = false
					resp, err := transport.Do(context.Background(), "/im/conversation/info", "application/json; encoding=utf-8", nil)
					if err != nil {
						t.Fatalf("session could not be reused: %v", err)
					}
					defer resp.Body.Close()
					if _, err := io.ReadAll(resp.Body); err != nil {
						t.Fatalf("reused session body: %v", err)
					}
				}
			})
		})
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
		body := webtransport.NewBody(context.Background(), func(context.Context) (webtransport.Chunk, error) {
			return webtransport.Chunk{Done: true, Data: base64.StdEncoding.EncodeToString([]byte("final bytes"))}, nil
		}, unavailable)
		data, err := io.ReadAll(body)
		body.Close()
		if err != nil || string(data) != "final bytes" {
			t.Fatalf("normal EOF corrupted by teardown: %q, %v", data, err)
		}
	}
}

func TestBrowserBodySanitizesPageAndEncodingFailures(t *testing.T) {
	for _, broken := range []bool{false, true} {
		body := webtransport.NewBody(context.Background(), func(context.Context) (webtransport.Chunk, error) {
			if broken {
				return webtransport.Chunk{Data: "invalid base64!"}, nil
			}
			return webtransport.Chunk{}, errors.New("private signed URL or cookie")
		}, unavailable)
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
				body := webtransport.NewBody(ctx, func(ctx context.Context) (webtransport.Chunk, error) {
					<-ctx.Done()
					close(stopped)
					return webtransport.Chunk{}, errors.New("private browser error after cancellation")
				}, unavailable)
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
