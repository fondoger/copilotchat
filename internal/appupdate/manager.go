// Package appupdate coordinates signed application updates without restarting
// the running app. It deliberately owns scheduling instead of registering the
// MyGo updater plugin, whose interval cannot be changed after construction.
package appupdate

import (
	"context"
	"errors"
	"sync"
	"time"
)

type Phase string

const (
	Idle        Phase = "idle"
	Checking    Phase = "checking"
	Available   Phase = "available"
	Downloading Phase = "downloading"
	Ready       Phase = "ready"
	Error       Phase = "error"
	Disabled    Phase = "disabled"
)

const (
	DefaultInterval = 24 * time.Hour
	startupDelay    = 10 * time.Second
	retryDelay      = 15 * time.Minute
	checkTimeout    = 45 * time.Second
	installTimeout  = 15 * time.Minute
	resumePoll      = time.Minute
)

// Snapshot is safe to retain. LastCheck is the last successful check, including
// one that found an update whose subsequent installation failed.
type Snapshot struct {
	Phase             Phase
	Version, Notes    string
	Error             string
	Downloaded, Total int64
	LastCheck         time.Time
}

// Release.Install must verify the update before replacing the installed app.
// Returning nil means the new version is installed and will run on next launch.
type Release struct {
	Version string
	Notes   string
	Install func(context.Context, func(downloaded, total int64)) error
}

type Client interface {
	Enabled() bool
	Check(context.Context) (*Release, error)
}

type operation struct {
	ctx       context.Context
	cancel    context.CancelFunc
	automatic bool
}

// Manager serializes checks and installations. The callback may run on any
// goroutine and is always called without the manager lock held. UI integrations
// should dispatch to their UI thread and then read the latest Snapshot.
type Manager struct {
	mu        sync.Mutex
	client    Client
	onChange  func()
	state     Snapshot
	release   *Release
	active    *operation
	automatic bool
	interval  time.Duration
	retryAt   time.Time
	startAt   time.Time
	running   bool
	closed    bool
	ctx       context.Context
	cancel    context.CancelFunc
	wake      chan struct{}
	done      chan struct{}
}

func New(client Client, onChange func()) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{
		client: client, onChange: onChange, interval: DefaultInterval,
		ctx: ctx, cancel: cancel, wake: make(chan struct{}, 1), done: make(chan struct{}),
		state: Snapshot{Phase: Idle},
	}
	if client == nil || !client.Enabled() {
		m.state.Phase = Disabled
	}
	return m
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.state
}

// Configure applies scheduling preferences immediately. Passing an older saved
// LastCheck does not overwrite a successful check completed in this session.
// Disabling automatic updates cancels only work started automatically.
func (m *Manager) Configure(automatic bool, interval time.Duration, lastCheck time.Time) {
	if interval <= 0 {
		interval = DefaultInterval
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.automatic, m.interval = automatic, interval
	changed := lastCheck.After(m.state.LastCheck)
	if changed {
		m.state.LastCheck = lastCheck
	}
	if !automatic && m.active != nil && m.active.automatic {
		m.active.cancel()
	}
	m.mu.Unlock()
	m.signal()
	if changed {
		m.notify()
	}
}

// Run blocks until ctx is canceled or Close is called. Start it once in a
// goroutine after the app is ready. Repeated calls return without another loop.
// The first automatic operation waits at least ten seconds; subsequent checks
// honor LastCheck and the currently configured interval. A one-minute maximum
// wait also catches up after the computer resumes from sleep.
func (m *Manager) Run(ctx context.Context) {
	if ctx.Err() != nil {
		m.Close()
		return
	}
	m.mu.Lock()
	if m.running || m.closed {
		m.mu.Unlock()
		return
	}
	m.running = true
	m.startAt = time.Now().Add(startupDelay)
	m.mu.Unlock()
	timer := time.NewTimer(resumePoll)
	defer timer.Stop()
	for {
		op, release, delay := m.schedule(time.Now())
		if op != nil {
			m.notify()
			if release != nil {
				go m.install(op, release)
			} else {
				go m.check(op)
			}
		}
		timer.Reset(delay)
		select {
		case <-ctx.Done():
			m.Close()
			return
		case <-m.ctx.Done():
			return
		case <-m.wake:
		case <-timer.C:
		}
		if !timer.Stop() {
			select {
			case <-timer.C:
			default:
			}
		}
	}
}

// schedule decides and reserves work under one lock, so a manual action cannot
// race an automatic check into a second operation.
func (m *Manager) schedule(now time.Time) (*operation, *Release, time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.automatic || !m.canStartLocked() {
		return nil, nil, resumePoll
	}
	due := m.startAt
	// A known, available release can be installed as soon as automatic updates
	// are enabled. After an error, recheck the feed when due before retrying.
	installAvailable := m.state.Phase == Available && m.release != nil
	// If the wall clock moved backward since the saved check, treat it as
	// due. Waiting for that future timestamp could postpone updates for years.
	if !installAvailable && !m.state.LastCheck.IsZero() && !m.state.LastCheck.After(now) {
		due = later(due, m.state.LastCheck.Add(m.interval))
	}
	due = later(due, m.retryAt)
	if now.Before(due) {
		return nil, nil, min(due.Sub(now), resumePoll)
	}
	if installAvailable {
		return m.beginLocked(true, Downloading), m.release, resumePoll
	}
	return m.beginLocked(true, Checking), nil, resumePoll
}

// Check starts a manual check and returns immediately. It works when automatic
// updates are off. Calls during existing work, or after installation, are no-ops.
func (m *Manager) Check() {
	m.mu.Lock()
	if !m.canStartLocked() {
		m.mu.Unlock()
		return
	}
	op := m.beginLocked(false, Checking)
	m.mu.Unlock()
	m.notify()
	go m.check(op)
}

// Install starts installing an available release and returns immediately. A
// failed install can be retried; a successful one cannot be installed twice.
func (m *Manager) Install() {
	m.mu.Lock()
	if !m.canStartLocked() || m.release == nil {
		m.mu.Unlock()
		return
	}
	release := m.release
	op := m.beginLocked(false, Downloading)
	m.mu.Unlock()
	m.notify()
	go m.install(op, release)
}

func (m *Manager) canStartLocked() bool {
	return !m.closed && m.active == nil && m.state.Phase != Ready && m.state.Phase != Disabled
}

func (m *Manager) beginLocked(automatic bool, phase Phase) *operation {
	ctx, cancel := context.WithCancel(m.ctx)
	op := &operation{ctx: ctx, cancel: cancel, automatic: automatic}
	m.active = op
	m.state.Phase, m.state.Error = phase, ""
	m.state.Downloaded, m.state.Total = 0, 0
	if phase == Checking {
		m.release = nil
		m.state.Version, m.state.Notes = "", ""
	}
	return op
}

func (m *Manager) check(op *operation) {
	ctx, cancel := context.WithTimeout(op.ctx, checkTimeout)
	release, err := m.client.Check(ctx)
	cancel()
	m.mu.Lock()
	if op.ctx.Err() != nil {
		m.canceledLocked()
	} else if err != nil {
		m.failedLocked(err)
	} else {
		m.state.LastCheck, m.retryAt = time.Now(), time.Time{}
		m.state.Phase = Idle
		if release != nil {
			copy := *release
			m.release = &copy
			m.state.Phase, m.state.Version, m.state.Notes = Available, copy.Version, copy.Notes
		}
	}
	if op.ctx.Err() == nil && err == nil && m.release != nil && op.automatic && m.automatic {
		release = m.release
		m.state.Phase = Downloading
		m.mu.Unlock()
		m.notify()
		m.install(op, release)
		return
	}
	m.finishedLocked(op)
	m.mu.Unlock()
	m.notify()
	m.signal()
}

func (m *Manager) install(op *operation, release *Release) {
	ctx, cancel := context.WithTimeout(op.ctx, installTimeout)
	err := ctx.Err()
	if err == nil {
		if release.Install == nil {
			err = errors.New("update has no installer")
		} else {
			err = release.Install(ctx, func(downloaded, total int64) {
				m.mu.Lock()
				if m.active != op || m.state.Phase != Downloading || ctx.Err() != nil {
					m.mu.Unlock()
					return
				}
				m.state.Downloaded, m.state.Total = downloaded, total
				m.mu.Unlock()
				m.notify()
			})
		}
	}
	cancel()
	m.mu.Lock()
	switch {
	case err == nil:
		// MyGo's final filesystem replacement may finish after cancellation.
		// Never hide that success or attempt another install over it.
		m.state.Phase, m.state.Error = Ready, ""
		m.retryAt = time.Time{}
	case op.ctx.Err() != nil:
		m.canceledLocked()
	default:
		m.failedLocked(err)
	}
	m.finishedLocked(op)
	m.mu.Unlock()
	m.notify()
	m.signal()
}

func (m *Manager) canceledLocked() {
	m.state.Phase, m.state.Error = Idle, ""
	m.state.Downloaded, m.state.Total = 0, 0
	if m.release != nil {
		m.state.Phase = Available
	}
}

func (m *Manager) failedLocked(err error) {
	m.state.Phase, m.state.Error = Error, err.Error()
	m.retryAt = time.Now().Add(retryDelay)
}

// Close cancels all work, including manual operations, without waiting for a
// network request or filesystem replacement to finish. Wait for Done before
// exiting the process: MyGo's final filesystem swap cannot be interrupted.
func (m *Manager) Close() {
	m.mu.Lock()
	if !m.closed {
		m.closed = true
		m.cancel()
		if m.active == nil {
			close(m.done)
		}
	}
	m.mu.Unlock()
}

// Done closes after Close has been called and any active operation has actually
// returned. A GUI quit handler should call Close, postpone quitting if Done is
// not closed yet, then wait in a goroutine and dispatch the final quit to its UI
// thread. This also avoids a race between checking the phase and closing.
func (m *Manager) Done() <-chan struct{} { return m.done }

func (m *Manager) finishedLocked(op *operation) {
	m.active = nil
	op.cancel()
	if m.closed {
		close(m.done)
	}
}

func (m *Manager) signal() {
	select {
	case m.wake <- struct{}{}:
	default:
	}
}

func (m *Manager) notify() {
	m.mu.Lock()
	callback, closed := m.onChange, m.closed
	m.mu.Unlock()
	if callback != nil && !closed {
		callback()
	}
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}
