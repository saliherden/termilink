package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// DefaultStateFile returns the default session state location
// (~/.termilink/state.json), or "" when there is no usable home directory.
// Prefer ResolveStateFile: an empty result is silently ignored by
// Manager.Save, which keeps sessions in memory, so a caller that skips the
// check starts an agent that looks healthy and forgets everything on restart.
func DefaultStateFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".termilink", "state.json")
}

// ResolveStateFile returns the state file to use, preferring an explicit
// override over the home-directory default. The override must be an absolute
// path; relative paths are rejected rather than resolved against the process
// working directory, which is not a meaningful base for a service. The error
// is returned instead of an empty string so a misconfiguration surfaces at
// startup instead of degrading into lost sessions.
func ResolveStateFile(override string) (string, error) {
	if override != "" {
		if !filepath.IsAbs(override) {
			return "", fmt.Errorf("state_file must be an absolute path (got %q)", override)
		}
		return override, nil
	}
	path := DefaultStateFile()
	if path == "" {
		return "", errors.New("no home directory; set state_file to an absolute path")
	}
	return path, nil
}

type State struct {
	ID      string `json:"id"`
	Cwd     string `json:"cwd"`
	Project string `json:"project,omitempty"`
	LastCmd string `json:"last_cmd"`
	PID     int    `json:"-"`
}

type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*State
	stateFile string

	// active holds which sessions are running a command. It lives on the
	// Manager rather than in State for two reasons. State is copied by value —
	// List hands out snapshots and Save marshals a copy — and a struct carrying
	// a mutex cannot be copied. And the bot library dispatches every update on
	// its own goroutine, so /stop and /status read this flag from a different
	// goroutine than the one running the command writes it, which is not
	// something the state file can express at all: Active was never persisted,
	// so a crashed process always comes back with no command running.
	activeMu sync.RWMutex
	active   map[string]bool
}

// SetActive records whether a command is currently running in the named session.
func (m *Manager) SetActive(id string, v bool) {
	m.activeMu.Lock()
	m.active[id] = v
	m.activeMu.Unlock()
}

// IsActive reports whether a command is currently running in the named session.
func (m *Manager) IsActive(id string) bool {
	m.activeMu.RLock()
	defer m.activeMu.RUnlock()
	return m.active[id]
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*State{}, active: map[string]bool{}}
}

// StateFile returns the resolved path sessions are persisted to, or "" when
// they are kept in memory only.
func (m *Manager) StateFile() string { return m.stateFile }

func NewManagerWithStateFile(path string) *Manager {
	m := NewManager()
	if path != "" {
		m.stateFile = path
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			var list []State
			if json.Unmarshal(data, &list) == nil {
				for i := range list {
					m.sessions[list[i].ID] = &list[i]
				}
			}
		}
	}
	return m
}

// Snapshot returns a copy of the named session's state. The copy is the point:
// a State handed out as a pointer is a live reference the caller can read and
// write with no lock held, and the bot library dispatches every update on its
// own goroutine, so /status and the command it is reporting on are reading and
// writing the same struct at the same time.
func (m *Manager) Snapshot(id string) (State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	if !ok {
		return State{}, false
	}
	return *s, true
}

// Ensure returns the state for id, creating and persisting it if it is new.
// The write error is reported rather than dropped: a session that could not be
// saved comes back empty after a restart while the audit log still reads "ok".
func (m *Manager) Ensure(id string) (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return *s, nil
	}
	s := &State{ID: id}
	m.sessions[id] = s
	return *s, m.saveLocked()
}

// Mutate applies fn to the named session under the write lock, creating it if
// it does not exist yet, and persists the result. fn is handed a pointer to the
// live State and must not retain it: the lock is released when fn returns.
// Saving inside the same critical section is what keeps the file and the memory
// from disagreeing — a caller that mutated and then called Save separately
// could interleave with another goroutine's write in between.
func (m *Manager) Mutate(id string, fn func(*State)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[id]
	if !ok {
		s = &State{ID: id}
		m.sessions[id] = s
	}
	fn(s)
	return m.saveLocked()
}

// Save writes the session list to the state file, reporting any failure to the
// caller. A gateway that silently drops this error loses every session on
// restart while its audit log still reads "ok", so callers should log what
// comes back.
func (m *Manager) Save() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.saveLocked()
}

func (m *Manager) saveLocked() error {
	if m.stateFile == "" {
		return nil
	}
	list := make([]State, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(m.stateFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(m.stateFile, data, 0o644)
}

func (m *Manager) List() []*State {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*State, 0, len(m.sessions))
	for _, s := range m.sessions {
		cp := *s
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (m *Manager) Remove(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}
