package docker

import (
	"context"
	"fmt"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"github.com/jota2rz/odoo-manager/internal/store"
)

// Manager handles Docker operations for Odoo and Postgres containers
type Manager struct {
	cli *client.Client
}

// NewManager creates a new Docker manager
func NewManager() (*Manager, error) {
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("failed to create Docker client: %w", err)
	}

	return &Manager{cli: cli}, nil
}

// StartProject starts Odoo and Postgres containers for a project
func (m *Manager) StartProject(ctx context.Context, project *store.Project) error {
	// Start Postgres container first
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)
	postgresConfig := &container.Config{
		Image: fmt.Sprintf("postgres:%s", project.PostgresVersion),
		Env: []string{
			"POSTGRES_DB=postgres",
			"POSTGRES_USER=odoo",
			"POSTGRES_PASSWORD=odoo",
		},
	}

	postgresHostConfig := &container.HostConfig{}

	// Check if postgres container exists
	postgresExists := m.containerExists(ctx, postgresContainerName)
	if !postgresExists {
		// Pull postgres image if not exists
		if err := m.pullImage(ctx, postgresConfig.Image); err != nil {
			return fmt.Errorf("failed to pull postgres image: %w", err)
		}

		// Create postgres container
		_, err := m.cli.ContainerCreate(ctx, postgresConfig, postgresHostConfig, nil, nil, postgresContainerName)
		if err != nil {
			return fmt.Errorf("failed to create postgres container: %w", err)
		}
	}

	// Start postgres container
	if err := m.cli.ContainerStart(ctx, postgresContainerName, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start postgres container: %w", err)
	}

	// Start Odoo container
	odooContainerName := fmt.Sprintf("odoo-%s", project.ID)
	odooConfig := &container.Config{
		Image: fmt.Sprintf("odoo:%s", project.OdooVersion),
		Env: []string{
			"HOST=postgres",
			"USER=odoo",
			"PASSWORD=odoo",
		},
		ExposedPorts: nat.PortSet{
			"8069/tcp": struct{}{},
		},
	}

	odooHostConfig := &container.HostConfig{
		Links: []string{fmt.Sprintf("%s:postgres", postgresContainerName)},
		PortBindings: nat.PortMap{
			"8069/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", project.Port)},
			},
		},
	}

	// Check if odoo container exists
	odooExists := m.containerExists(ctx, odooContainerName)
	if !odooExists {
		// Pull odoo image if not exists
		if err := m.pullImage(ctx, odooConfig.Image); err != nil {
			return fmt.Errorf("failed to pull odoo image: %w", err)
		}

		// Create odoo container
		_, err := m.cli.ContainerCreate(ctx, odooConfig, odooHostConfig, nil, nil, odooContainerName)
		if err != nil {
			return fmt.Errorf("failed to create odoo container: %w", err)
		}
	}

	// Start odoo container
	if err := m.cli.ContainerStart(ctx, odooContainerName, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start odoo container: %w", err)
	}

	return nil
}

// StopProject stops containers for a project
func (m *Manager) StopProject(ctx context.Context, project *store.Project) error {
	odooContainerName := fmt.Sprintf("odoo-%s", project.ID)
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)

	// Stop Odoo container
	timeout := 30
	if err := m.cli.ContainerStop(ctx, odooContainerName, container.StopOptions{Timeout: &timeout}); err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to stop odoo container: %w", err)
		}
	}

	// Stop Postgres container
	if err := m.cli.ContainerStop(ctx, postgresContainerName, container.StopOptions{Timeout: &timeout}); err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to stop postgres container: %w", err)
		}
	}

	return nil
}

// GetLogs streams logs from a container
func (m *Manager) GetLogs(ctx context.Context, projectID string, containerType string) (io.ReadCloser, error) {
	containerName := fmt.Sprintf("%s-%s", containerType, projectID)

	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "100",
	}

	logs, err := m.cli.ContainerLogs(ctx, containerName, options)
	if err != nil {
		return nil, fmt.Errorf("failed to get logs: %w", err)
	}

	return logs, nil
}

// GetProjectStatus returns the status of project containers
func (m *Manager) GetProjectStatus(ctx context.Context, projectID string) (string, error) {
	odooContainerName := fmt.Sprintf("odoo-%s", projectID)

	inspect, err := m.cli.ContainerInspect(ctx, odooContainerName)
	if err != nil {
		if client.IsErrNotFound(err) {
			return "stopped", nil
		}
		return "error", err
	}

	if inspect.State.Running {
		return "running", nil
	}

	return "stopped", nil
}

// RemoveProject removes containers for a project
func (m *Manager) RemoveProject(ctx context.Context, project *store.Project) error {
	odooContainerName := fmt.Sprintf("odoo-%s", project.ID)
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)

	// Stop containers first
	if err := m.StopProject(ctx, project); err != nil {
		return err
	}

	// Remove Odoo container
	if err := m.cli.ContainerRemove(ctx, odooContainerName, container.RemoveOptions{Force: true}); err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to remove odoo container: %w", err)
		}
	}

	// Remove Postgres container
	if err := m.cli.ContainerRemove(ctx, postgresContainerName, container.RemoveOptions{Force: true}); err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("failed to remove postgres container: %w", err)
		}
	}

	return nil
}

// containerExists checks if a container exists
func (m *Manager) containerExists(ctx context.Context, name string) bool {
	_, err := m.cli.ContainerInspect(ctx, name)
	return err == nil
}

// pullImage pulls a Docker image
func (m *Manager) pullImage(ctx context.Context, imageName string) error {
	reader, err := m.cli.ImagePull(ctx, imageName, image.PullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()

	// Read the response to ensure the pull completes
	_, err = io.Copy(io.Discard, reader)
	return err
}

// GenerateDockerCompose generates a docker-compose.yml file for a project
func GenerateDockerCompose(project *store.Project) string {
	return fmt.Sprintf(`version: '3.8'

services:
  postgres:
    image: postgres:%s
    container_name: postgres-%s
    environment:
      - POSTGRES_DB=postgres
      - POSTGRES_USER=odoo
      - POSTGRES_PASSWORD=odoo
    volumes:
      - postgres-data:/var/lib/postgresql/data
    networks:
      - odoo-network

  odoo:
    image: odoo:%s
    container_name: odoo-%s
    depends_on:
      - postgres
    ports:
      - "%d:8069"
    environment:
      - HOST=postgres
      - USER=odoo
      - PASSWORD=odoo
    volumes:
      - odoo-data:/var/lib/odoo
      - ./addons:/mnt/extra-addons
    networks:
      - odoo-network

volumes:
  postgres-data:
  odoo-data:

networks:
  odoo-network:
    driver: bridge
`, project.PostgresVersion, project.ID, project.OdooVersion, project.ID, project.Port)
}
