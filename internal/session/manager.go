package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

func DefaultStateFile() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".termilink", "state.json")
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

func (m *Manager) Get(id string) (*State, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

func (m *Manager) Ensure(id string) *State {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.sessions[id]; ok {
		return s
	}
	s := &State{ID: id}
	m.sessions[id] = s
	m.saveLocked()
	return s
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
