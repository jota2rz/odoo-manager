package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// Project represents an Odoo project configuration
type Project struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Description     string    `json:"description"`
	OdooVersion     string    `json:"odoo_version"`
	PostgresVersion string    `json:"postgres_version"`
	Port            int       `json:"port"`
	Status          string    `json:"status"` // running, stopped, error
	CreatedAt       time.Time `json:"created_at"`
	UpdatedAt       time.Time `json:"updated_at"`
}

// ProjectStore manages projects persistence using SQLite
type ProjectStore struct {
	db *sql.DB
}

// NewProjectStore creates a new project store backed by SQLite
func NewProjectStore(dbPath string) (*ProjectStore, error) {
	// Ensure directory exists
	dir := filepath.Dir(dbPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	// Limit to 1 open connection to avoid SQLITE_BUSY on concurrent writes
	db.SetMaxOpenConns(1)

	// Enable WAL mode for better concurrent read performance
	if _, err := db.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		db.Close()
		return nil, err
	}

	// Set busy timeout to wait up to 5 seconds instead of failing immediately
	if _, err := db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		db.Close()
		return nil, err
	}

	// Create projects table if it doesn't exist
	_, err = db.Exec(`
		CREATE TABLE IF NOT EXISTS projects (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			description TEXT NOT NULL DEFAULT '',
			odoo_version TEXT NOT NULL,
			postgres_version TEXT NOT NULL,
			port INTEGER NOT NULL,
			status TEXT NOT NULL DEFAULT 'stopped',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)
	`)
	if err != nil {
		db.Close()
		return nil, err
	}

	return &ProjectStore{db: db}, nil
}

// Close closes the underlying database connection
func (s *ProjectStore) Close() error {
	return s.db.Close()
}

// Create adds a new project
func (s *ProjectStore) Create(project *Project) error {
	now := time.Now()
	project.CreatedAt = now
	project.UpdatedAt = now

	_, err := s.db.Exec(
		`INSERT INTO projects (id, name, description, odoo_version, postgres_version, port, status, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		project.ID, project.Name, project.Description, project.OdooVersion,
		project.PostgresVersion, project.Port, project.Status, project.CreatedAt, project.UpdatedAt,
	)
	return err
}

// Get retrieves a project by ID
func (s *ProjectStore) Get(id string) (*Project, bool) {
	p := &Project{}
	err := s.db.QueryRow(
		`SELECT id, name, description, odoo_version, postgres_version, port, status, created_at, updated_at
		 FROM projects WHERE id = ?`, id,
	).Scan(&p.ID, &p.Name, &p.Description, &p.OdooVersion, &p.PostgresVersion,
		&p.Port, &p.Status, &p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, false
	}
	return p, true
}

// List returns all projects
func (s *ProjectStore) List() []*Project {
	rows, err := s.db.Query(
		`SELECT id, name, description, odoo_version, postgres_version, port, status, created_at, updated_at
		 FROM projects ORDER BY created_at DESC`)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var projects []*Project
	for rows.Next() {
		p := &Project{}
		if err := rows.Scan(&p.ID, &p.Name, &p.Description, &p.OdooVersion, &p.PostgresVersion,
			&p.Port, &p.Status, &p.CreatedAt, &p.UpdatedAt); err != nil {
			continue
		}
		projects = append(projects, p)
	}
	return projects
}

// Update modifies an existing project
func (s *ProjectStore) Update(project *Project) error {
	project.UpdatedAt = time.Now()

	result, err := s.db.Exec(
		`UPDATE projects SET name=?, description=?, odoo_version=?, postgres_version=?, port=?, status=?, updated_at=?
		 WHERE id=?`,
		project.Name, project.Description, project.OdooVersion, project.PostgresVersion,
		project.Port, project.Status, project.UpdatedAt, project.ID,
	)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// Delete removes a project
func (s *ProjectStore) Delete(id string) error {
	_, err := s.db.Exec(`DELETE FROM projects WHERE id = ?`, id)
	return err
}
