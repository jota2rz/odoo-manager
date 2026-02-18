package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Project represents an Odoo project configuration
type Project struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	OdooVersion string    `json:"odoo_version"`
	PostgresVersion string `json:"postgres_version"`
	Port        int       `json:"port"`
	Status      string    `json:"status"` // running, stopped, error
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
}

// ProjectStore manages projects persistence
type ProjectStore struct {
	mu       sync.RWMutex
	projects map[string]*Project
	filePath string
}

// NewProjectStore creates a new project store
func NewProjectStore(filePath string) (*ProjectStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	store := &ProjectStore{
		projects: make(map[string]*Project),
		filePath: filePath,
	}

	// Load existing projects if file exists
	if _, err := os.Stat(filePath); err == nil {
		if err := store.load(); err != nil {
			return nil, err
		}
	}

	return store, nil
}

// Create adds a new project
func (s *ProjectStore) Create(project *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	project.CreatedAt = time.Now()
	project.UpdatedAt = time.Now()
	s.projects[project.ID] = project

	return s.save()
}

// Get retrieves a project by ID
func (s *ProjectStore) Get(id string) (*Project, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	project, ok := s.projects[id]
	return project, ok
}

// List returns all projects
func (s *ProjectStore) List() []*Project {
	s.mu.RLock()
	defer s.mu.RUnlock()

	projects := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p)
	}
	return projects
}

// Update modifies an existing project
func (s *ProjectStore) Update(project *Project) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, ok := s.projects[project.ID]; !ok {
		return os.ErrNotExist
	}

	project.UpdatedAt = time.Now()
	s.projects[project.ID] = project

	return s.save()
}

// Delete removes a project
func (s *ProjectStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.projects, id)
	return s.save()
}

// load reads projects from file
func (s *ProjectStore) load() error {
	data, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}

	var projects []*Project
	if err := json.Unmarshal(data, &projects); err != nil {
		return err
	}

	s.projects = make(map[string]*Project)
	for _, p := range projects {
		s.projects[p.ID] = p
	}

	return nil
}

// save writes projects to file
func (s *ProjectStore) save() error {
	projects := make([]*Project, 0, len(s.projects))
	for _, p := range s.projects {
		projects = append(projects, p)
	}

	data, err := json.MarshalIndent(projects, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(s.filePath, data, 0644)
}
