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
	Active  bool   `json:"active"`
	LastCmd string `json:"last_cmd"`
	PID     int    `json:"pid"`
}

type Manager struct {
	mu        sync.RWMutex
	sessions  map[string]*State
	stateFile string
}

func NewManager() *Manager {
	return &Manager{sessions: map[string]*State{}}
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

func (m *Manager) Save() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveLocked()
}

func (m *Manager) saveLocked() {
	if m.stateFile == "" {
		return
	}
	list := make([]State, 0, len(m.sessions))
	for _, s := range m.sessions {
		list = append(list, *s)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
	data, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(m.stateFile), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(m.stateFile, data, 0o644)
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