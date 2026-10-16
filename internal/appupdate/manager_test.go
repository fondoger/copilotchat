package appupdate

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

type fakeClient struct {
	mu      sync.Mutex
	enabled bool
	checks  int
	check   func(context.Context) (*Release, error)
}

func (f *fakeClient) Enabled() bool { return f.enabled }

func (f *fakeClient) Check(ctx context.Context) (*Release, error) {
	f.mu.Lock()
	f.checks++
	f.mu.Unlock()
	if f.check == nil {
		return nil, nil
	}
	return f.check(ctx)
}

func (f *fakeClient) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.checks
}

func testManager(client *fakeClient) *Manager {
	var manager *Manager
	// Every transition also verifies callbacks may read the state without
	// deadlocking. Real UI callbacks dispatch first, then read the latest state.
	manager = New(client, func() { manager.Snapshot() })
	return manager
}

func requirePhase(t *testing.T, m *Manager, want Phase) Snapshot {
	t.Helper()
	synctest.Wait()
	s := m.Snapshot()
	if s.Phase != want {
		t.Fatalf("phase = %s, want %s; snapshot: %+v", s.Phase, want, s)
	}
	return s
}

func TestAutomaticStartupAndReady(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan struct{})
		installs := 0
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Notes: "Release notes", Install: func(ctx context.Context, progress func(int64, int64)) error {
				installs++
				progress(20, 100)
				select {
				case <-finish:
					progress(100, 100)
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}}, nil
		}}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, time.Hour, time.Time{})
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(9 * time.Second)
		requirePhase(t, m, Idle)
		if client.count() != 0 {
			t.Fatal("checked before the startup delay")
		}
		time.Sleep(time.Second)
		s := requirePhase(t, m, Downloading)
		if s.Version != "0.4.0" || s.Notes != "Release notes" || s.Downloaded != 20 || s.Total != 100 || s.LastCheck.IsZero() {
			t.Fatalf("missing update metadata or progress: %+v", s)
		}
		m.Check()
		m.Install()
		synctest.Wait()
		if client.count() != 1 || installs != 1 {
			t.Fatal("started overlapping work")
		}
		close(finish)
		requirePhase(t, m, Ready)
		m.Check()
		m.Install()
		m.Configure(true, time.Nanosecond, time.Time{})
		time.Sleep(2 * time.Hour)
		requirePhase(t, m, Ready)
		if client.count() != 1 || installs != 1 {
			t.Fatalf("installed over a pending restart: checks=%d installs=%d", client.count(), installs)
		}
	})
}

func TestPersistedLastCheckAndFrequencyChanges(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{enabled: true}
		m := testManager(client)
		defer m.Close()
		lastCheck := time.Now().Add(-30 * time.Minute)
		m.Configure(true, time.Hour, lastCheck)
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(20 * time.Minute)
		if client.count() != 0 {
			t.Fatal("ignored the saved successful check")
		}
		m.Configure(true, 10*time.Minute, lastCheck)
		s := requirePhase(t, m, Idle)
		if client.count() != 1 || !s.LastCheck.Equal(time.Now()) {
			t.Fatalf("shorter frequency did not check immediately: checks=%d state=%+v", client.count(), s)
		}
		m.Configure(true, 2*time.Hour, lastCheck)
		time.Sleep(time.Hour)
		synctest.Wait()
		if client.count() != 1 || !m.Snapshot().LastCheck.Equal(s.LastCheck) {
			t.Fatal("longer frequency or stale saved LastCheck was ignored")
		}
		m.Configure(false, time.Minute, lastCheck)
		time.Sleep(3 * time.Hour)
		synctest.Wait()
		if client.count() != 1 {
			t.Fatal("automatic checks continued after disabling")
		}
		m.Check()
		requirePhase(t, m, Idle)
		if client.count() != 2 {
			t.Fatal("manual checking was disabled with automatic checks")
		}
	})
}

func TestManualCheckAndInstallSignatureFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installs := 0
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Install: func(context.Context, func(int64, int64)) error {
				installs++
				if installs == 1 {
					return errors.New("signature verification failed")
				}
				return nil
			}}, nil
		}}
		m := testManager(client)
		defer m.Close()
		m.Check()
		available := requirePhase(t, m, Available)
		if installs != 0 || available.LastCheck.IsZero() {
			t.Fatal("manual check installed without a request or failed to record success")
		}
		m.Install()
		failed := requirePhase(t, m, Error)
		if failed.Error != "signature verification failed" || failed.Version != available.Version || !failed.LastCheck.Equal(available.LastCheck) {
			t.Fatalf("failed verification lost its status: %+v", failed)
		}
		m.Install()
		requirePhase(t, m, Ready)
		m.Install()
		synctest.Wait()
		if installs != 2 {
			t.Fatalf("manual retry count = %d, want 2", installs)
		}
	})
}

func TestAutomaticFailureBackoff(t *testing.T) {
	for _, installFailure := range []bool{false, true} {
		name := "check"
		if installFailure {
			name = "signature"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &fakeClient{enabled: true}
				client.check = func(context.Context) (*Release, error) {
					if !installFailure && client.count() == 1 {
						return nil, errors.New("network unavailable")
					}
					if installFailure {
						return &Release{Version: "0.4.0", Install: func(context.Context, func(int64, int64)) error {
							if client.count() == 1 {
								return errors.New("signature verification failed")
							}
							return nil
						}}, nil
					}
					return nil, nil
				}
				m := testManager(client)
				defer m.Close()
				m.Configure(true, time.Minute, time.Time{})
				go m.Run(context.Background())
				synctest.Wait()
				time.Sleep(startupDelay)
				s := requirePhase(t, m, Error)
				if s.LastCheck.IsZero() == installFailure {
					t.Fatalf("LastCheck must reflect checks, not installations: %+v", s)
				}
				time.Sleep(retryDelay - time.Second)
				synctest.Wait()
				if client.count() != 1 {
					t.Fatalf("retried before fifteen minutes: %d checks", client.count())
				}
				time.Sleep(time.Second)
				want := Idle
				if installFailure {
					want = Ready
				}
				requirePhase(t, m, want)
				if client.count() != 2 {
					t.Fatalf("did not retry when due: %d checks", client.count())
				}
			})
		})
	}
}

func TestManualCheckBypassesErrorBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{enabled: true}
		client.check = func(context.Context) (*Release, error) {
			if client.count() == 1 {
				return nil, errors.New("network unavailable")
			}
			return nil, nil
		}
		m := testManager(client)
		defer m.Close()
		m.Check()
		requirePhase(t, m, Error)
		m.Check()
		requirePhase(t, m, Idle)
		if client.count() != 2 {
			t.Fatal("manual retry was delayed by the automatic retry policy")
		}
	})
}

func TestDisableCancelsAutomaticCheckButKeepsManualCheck(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finishManual := make(chan struct{})
		canceled := 0
		client := &fakeClient{enabled: true}
		client.check = func(ctx context.Context) (*Release, error) {
			if client.count() == 1 {
				<-ctx.Done()
				canceled++
				return nil, ctx.Err()
			}
			select {
			case <-finishManual:
				return nil, nil
			case <-ctx.Done():
				canceled++
				return nil, ctx.Err()
			}
		}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, time.Hour, time.Time{})
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(startupDelay)
		requirePhase(t, m, Checking)
		m.Configure(false, time.Hour, time.Time{})
		s := requirePhase(t, m, Idle)
		if canceled != 1 || !s.LastCheck.IsZero() || s.Error != "" {
			t.Fatalf("cancellation was treated as success or failure: %+v, cancellations=%d", s, canceled)
		}
		m.Check()
		requirePhase(t, m, Checking)
		m.Configure(false, time.Hour, time.Time{})
		requirePhase(t, m, Checking)
		if canceled != 1 {
			t.Fatal("configuration canceled a manual check")
		}
		close(finishManual)
		requirePhase(t, m, Idle)
	})
}

func TestDisableCancelsAutomaticInstallButKeepsManualInstall(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installs, cancellations := 0, 0
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Install: func(ctx context.Context, progress func(int64, int64)) error {
				installs++
				progress(20, 100)
				<-ctx.Done()
				cancellations++
				return ctx.Err()
			}}, nil
		}}
		m := testManager(client)
		defer m.Close()
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		m.Configure(true, time.Hour, time.Time{})
		go m.Run(ctx)
		synctest.Wait()
		time.Sleep(startupDelay)
		requirePhase(t, m, Downloading)
		m.Configure(false, time.Hour, time.Time{})
		s := requirePhase(t, m, Available)
		if cancellations != 1 || s.Downloaded != 0 || s.Error != "" {
			t.Fatalf("automatic installation was not cleanly canceled: %+v", s)
		}
		m.Install()
		requirePhase(t, m, Downloading)
		m.Configure(false, time.Hour, time.Time{})
		requirePhase(t, m, Downloading)
		if cancellations != 1 || installs != 2 {
			t.Fatal("configuration canceled the manual installation")
		}
		cancel()
		requirePhase(t, m, Available)
		if cancellations != 2 {
			t.Fatal("app shutdown did not cancel the manual installation")
		}
		m.Check()
		m.Install()
		synctest.Wait()
		if client.count() != 1 || installs != 2 {
			t.Fatal("work restarted after shutdown")
		}
	})
}

func TestSuccessfulReplacementWinsLateCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Install: func(ctx context.Context, _ func(int64, int64)) error {
				<-ctx.Done()
				// The filesystem swap already finished when the user disabled
				// automatic updates. Its successful result must not be lost.
				return nil
			}}, nil
		}}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, time.Hour, time.Time{})
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(startupDelay)
		requirePhase(t, m, Downloading)
		m.Configure(false, time.Hour, time.Time{})
		requirePhase(t, m, Ready)
		m.Check()
		m.Install()
		requirePhase(t, m, Ready)
	})
}

func TestConcurrentRequestsAndDelayedCancellationStaySerialized(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finish := make(chan struct{})
		client := &fakeClient{enabled: true, check: func(ctx context.Context) (*Release, error) {
			<-ctx.Done()
			<-finish
			return nil, ctx.Err()
		}}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, time.Hour, time.Time{})
		go m.Run(context.Background())
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(startupDelay)
		requirePhase(t, m, Checking)
		m.Configure(false, time.Hour, time.Time{})
		for range 20 {
			go m.Check()
			go m.Install()
		}
		synctest.Wait()
		if client.count() != 1 {
			t.Fatal("started another check before the canceled operation exited")
		}
		close(finish)
		requirePhase(t, m, Idle)
	})
}

func TestOperationTimeouts(t *testing.T) {
	for _, installing := range []bool{false, true} {
		name := "check"
		if installing {
			name = "install"
		}
		t.Run(name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				client := &fakeClient{enabled: true, check: func(ctx context.Context) (*Release, error) {
					if installing {
						return &Release{Version: "0.4.0", Install: func(ctx context.Context, _ func(int64, int64)) error {
							<-ctx.Done()
							return ctx.Err()
						}}, nil
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}}
				m := testManager(client)
				defer m.Close()
				m.Check()
				delay := checkTimeout
				if installing {
					requirePhase(t, m, Available)
					m.Install()
					delay = installTimeout
				}
				synctest.Wait()
				time.Sleep(delay)
				s := requirePhase(t, m, Error)
				if !strings.Contains(s.Error, "deadline exceeded") {
					t.Fatalf("timeout was hidden: %+v", s)
				}
			})
		})
	}
}

func TestEnablingAutomaticInstallsAnAvailableRelease(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		installs := 0
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Install: func(context.Context, func(int64, int64)) error {
				installs++
				return nil
			}}, nil
		}}
		m := testManager(client)
		defer m.Close()
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(startupDelay)
		m.Check()
		requirePhase(t, m, Available)
		m.Configure(true, time.Hour, time.Time{})
		requirePhase(t, m, Ready)
		if client.count() != 1 || installs != 1 {
			t.Fatal("enabling automatic updates failed to install the known release")
		}
	})
}

func TestDisabledBuildNeverCallsClient(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, time.Minute, time.Time{})
		go m.Run(context.Background())
		m.Check()
		m.Install()
		time.Sleep(time.Hour)
		requirePhase(t, m, Disabled)
		if client.count() != 0 {
			t.Fatal("disabled build attempted network work")
		}
	})
}

func TestFutureLastCheckIsDueAfterStartupDelay(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client := &fakeClient{enabled: true}
		m := testManager(client)
		defer m.Close()
		m.Configure(true, DefaultInterval, time.Now().AddDate(5, 0, 0))
		go m.Run(context.Background())
		synctest.Wait()
		time.Sleep(startupDelay)
		s := requirePhase(t, m, Idle)
		if client.count() != 1 || !s.LastCheck.Equal(time.Now()) {
			t.Fatalf("future timestamp postponed checking: checks=%d state=%+v", client.count(), s)
		}
	})
}

func TestDoneWaitsForUninterruptibleReplacement(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		finishSwap := make(chan struct{})
		client := &fakeClient{enabled: true, check: func(context.Context) (*Release, error) {
			return &Release{Version: "0.4.0", Install: func(ctx context.Context, _ func(int64, int64)) error {
				<-ctx.Done()
				<-finishSwap
				return nil
			}}, nil
		}}
		m := testManager(client)
		m.Check()
		requirePhase(t, m, Available)
		m.Install()
		requirePhase(t, m, Downloading)
		m.Close()
		m.Close()
		synctest.Wait()
		select {
		case <-m.Done():
			t.Fatal("reported shutdown before the filesystem swap returned")
		default:
		}
		close(finishSwap)
		requirePhase(t, m, Ready)
		select {
		case <-m.Done():
		default:
			t.Fatal("did not finish shutdown after installation returned")
		}
		m.Close()
	})
}

func TestCloseWithoutWorkCompletesImmediately(t *testing.T) {
	m := New(&fakeClient{enabled: true}, nil)
	m.Close()
	m.Close()
	select {
	case <-m.Done():
	default:
		t.Fatal("idle manager did not close immediately")
	}
}
