package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/volume"
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

// Ping checks whether the Docker daemon is reachable.
func (m *Manager) Ping(ctx context.Context) error {
	_, err := m.cli.Ping(ctx)
	return err
}

// projectLabels returns the standard labels for a container managed by odoo-manager
func projectLabels(projectID string, role string) map[string]string {
	return map[string]string{
		"odoo-manager.project-id": projectID,
		"odoo-manager.role":       role,
		"odoo-manager.managed":    "true",
	}
}

// postgresImage returns the Docker image reference for PostgreSQL.
// Odoo 19+ requires pgvector extensions, so we use the pgvector/pgvector
// image (tags like pg16-trixie). Older Odoo versions use the standard
// postgres:{version} image.
func postgresImage(odooVersion string, pgVersion string) string {
	odooMajor := 0
	parts := strings.SplitN(odooVersion, ".", 2)
	if len(parts) > 0 {
		odooMajor, _ = strconv.Atoi(parts[0])
	}
	if odooMajor >= 19 {
		return fmt.Sprintf("pgvector/pgvector:pg%s-trixie", pgVersion)
	}
	return fmt.Sprintf("postgres:%s", pgVersion)
}

// CreateProject pulls images and creates containers for a project without starting them.
func (m *Manager) CreateProject(ctx context.Context, project *store.Project) error {
	// Create Postgres container
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)
	postgresConfig := &container.Config{
		Image: postgresImage(project.OdooVersion, project.PostgresVersion),
		Env: []string{
			"POSTGRES_DB=postgres",
			"POSTGRES_USER=odoo",
			"POSTGRES_PASSWORD=odoo",
		},
		Labels: projectLabels(project.ID, "postgres"),
	}
	postgresHostConfig := &container.HostConfig{}

	if !m.containerExists(ctx, postgresContainerName) {
		if err := m.pullImage(ctx, postgresConfig.Image); err != nil {
			return fmt.Errorf("failed to pull postgres image: %w", err)
		}
		if _, err := m.cli.ContainerCreate(ctx, postgresConfig, postgresHostConfig, nil, nil, postgresContainerName); err != nil {
			return fmt.Errorf("failed to create postgres container: %w", err)
		}
	}

	// Create Odoo container
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
		Tty:    true, // enable TTY so Odoo outputs ANSI colors in logs
		Labels: projectLabels(project.ID, "odoo"),
	}
	configVolumeName := fmt.Sprintf("odoo-config-%s", project.ID)
	odooHostConfig := &container.HostConfig{
		Links: []string{fmt.Sprintf("%s:postgres", postgresContainerName)},
		PortBindings: nat.PortMap{
			"8069/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", project.Port)},
			},
		},
		Binds: []string{
			fmt.Sprintf("%s:/etc/odoo", configVolumeName),
		},
	}

	if !m.containerExists(ctx, odooContainerName) {
		if err := m.pullImage(ctx, odooConfig.Image); err != nil {
			return fmt.Errorf("failed to pull odoo image: %w", err)
		}
		if _, err := m.cli.ContainerCreate(ctx, odooConfig, odooHostConfig, nil, nil, odooContainerName); err != nil {
			return fmt.Errorf("failed to create odoo container: %w", err)
		}
		// Seed the config volume with the default odoo.conf from the image.
		if err := m.seedOdooConfig(ctx, odooContainerName); err != nil {
			// Non-fatal: the container will still start with defaults.
			fmt.Printf("Warning: failed to seed odoo.conf for %s: %v\n", project.ID, err)
		}
	}

	return nil
}

// seedOdooConfig copies the default /etc/odoo/odoo.conf from the Odoo image
// into the config volume. It starts the container briefly to populate the
// volume, copies the default config out, then stops the container.
func (m *Manager) seedOdooConfig(ctx context.Context, containerName string) error {
	// Start the container briefly so the entrypoint writes the default config
	if err := m.cli.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
		return fmt.Errorf("failed to start container for seeding: %w", err)
	}

	// The entrypoint may need a moment to write /etc/odoo/odoo.conf
	// We'll just check if the file exists; if not, write a sensible default.
	_, _, err := m.cli.CopyFromContainer(ctx, containerName, "/etc/odoo/odoo.conf")
	if err != nil {
		// File doesn't exist yet — write a minimal default
		defaultConf := defaultOdooConf()
		if werr := m.writeFileToContainer(ctx, containerName, "/etc/odoo/odoo.conf", []byte(defaultConf)); werr != nil {
			return fmt.Errorf("failed to write default config: %w", werr)
		}
	}

	// Stop the container — CreateProject should leave containers stopped
	timeout := 10
	_ = m.cli.ContainerStop(ctx, containerName, container.StopOptions{Timeout: &timeout})

	return nil
}

// defaultOdooConf returns a minimal default odoo.conf.
func defaultOdooConf() string {
	return `[options]
addons_path = /mnt/extra-addons
data_dir = /var/lib/odoo
`
}

// StartProject starts Odoo and Postgres containers for a project
func (m *Manager) StartProject(ctx context.Context, project *store.Project) error {
	// Start Postgres container first
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)
	postgresConfig := &container.Config{
		Image: postgresImage(project.OdooVersion, project.PostgresVersion),
		Env: []string{
			"POSTGRES_DB=postgres",
			"POSTGRES_USER=odoo",
			"POSTGRES_PASSWORD=odoo",
		},
		Labels: projectLabels(project.ID, "postgres"),
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
		Tty:    true, // enable TTY so Odoo outputs ANSI colors in logs
		Labels: projectLabels(project.ID, "odoo"),
	}

	configVolumeName := fmt.Sprintf("odoo-config-%s", project.ID)
	odooHostConfig := &container.HostConfig{
		Links: []string{fmt.Sprintf("%s:postgres", postgresContainerName)},
		PortBindings: nat.PortMap{
			"8069/tcp": []nat.PortBinding{
				{HostIP: "0.0.0.0", HostPort: fmt.Sprintf("%d", project.Port)},
			},
		},
		Binds: []string{
			fmt.Sprintf("%s:/etc/odoo", configVolumeName),
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

	var firstErr error

	// Stop Odoo container
	timeout := 30
	if err := m.cli.ContainerStop(ctx, odooContainerName, container.StopOptions{Timeout: &timeout}); err != nil {
		if !client.IsErrNotFound(err) {
			firstErr = fmt.Errorf("failed to stop odoo container: %w", err)
		}
	}

	// Always attempt to stop Postgres even if Odoo stop failed
	if err := m.cli.ContainerStop(ctx, postgresContainerName, container.StopOptions{Timeout: &timeout}); err != nil {
		if !client.IsErrNotFound(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to stop postgres container: %w", err)
			}
		}
	}

	return firstErr
}

// GetLogs streams logs from a container. The returned boolean indicates
// whether the container has a TTY (raw stream) or not (multiplexed stream
// that must be demuxed with stdcopy).
func (m *Manager) GetLogs(ctx context.Context, projectID string, containerType string) (io.ReadCloser, bool, error) {
	containerName := fmt.Sprintf("%s-%s", containerType, projectID)

	// Inspect the container to check if it has a TTY
	inspect, err := m.cli.ContainerInspect(ctx, containerName)
	if err != nil {
		return nil, false, fmt.Errorf("failed to inspect container: %w", err)
	}
	hasTTY := inspect.Config.Tty

	options := container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       "100",
	}

	logs, err := m.cli.ContainerLogs(ctx, containerName, options)
	if err != nil {
		return nil, false, fmt.Errorf("failed to get logs: %w", err)
	}

	return logs, hasTTY, nil
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

// ReconcileStatus checks the actual Docker state and returns the real status,
// correcting any stale status stored in the project. Transient statuses like
// "creating" are preserved as-is because containers may not exist yet.
func (m *Manager) ReconcileStatus(ctx context.Context, project *store.Project) string {
	// Don't overwrite transient statuses — the async goroutine will
	// broadcast the real status once the operation completes.
	switch project.Status {
	case "creating", "deleting", "starting", "stopping":
		return project.Status
	}
	status, err := m.GetProjectStatus(ctx, project.ID)
	if err != nil {
		return "error"
	}
	return status
}

// RemoveProject removes containers for a project
func (m *Manager) RemoveProject(ctx context.Context, project *store.Project) error {
	odooContainerName := fmt.Sprintf("odoo-%s", project.ID)
	postgresContainerName := fmt.Sprintf("postgres-%s", project.ID)

	// Best-effort stop — don't bail out early so removal can proceed
	_ = m.StopProject(ctx, project)

	var firstErr error

	// Remove Odoo container
	if err := m.cli.ContainerRemove(ctx, odooContainerName, container.RemoveOptions{Force: true}); err != nil {
		if !client.IsErrNotFound(err) {
			firstErr = fmt.Errorf("failed to remove odoo container: %w", err)
		}
	}

	// Always attempt to remove Postgres even if Odoo remove failed
	if err := m.cli.ContainerRemove(ctx, postgresContainerName, container.RemoveOptions{Force: true}); err != nil {
		if !client.IsErrNotFound(err) {
			if firstErr == nil {
				firstErr = fmt.Errorf("failed to remove postgres container: %w", err)
			}
		}
	}

	return firstErr
}

// CleanupResult holds the outcome of a cleanup operation.
type CleanupResult struct {
	Removed []string `json:"removed"`
	Errors  []string `json:"errors"`
}

// newCleanupResult returns a CleanupResult with non-nil slices so JSON
// serialisation always produces [] instead of null.
func newCleanupResult() *CleanupResult {
	return &CleanupResult{Removed: []string{}, Errors: []string{}}
}

// isOwnedContainer reports whether the container belongs to a project that
// still exists in the store.  A container is considered owned when it carries
// the odoo-manager labels AND its project-id is present in knownProjectIDs.
func isOwnedContainer(c container.Summary, knownProjectIDs map[string]bool) bool {
	if c.Labels["odoo-manager.managed"] != "true" {
		return false
	}
	return knownProjectIDs[c.Labels["odoo-manager.project-id"]]
}

// containerDisplayName returns a human-readable name for a container.
func containerDisplayName(c container.Summary) string {
	if len(c.Names) > 0 {
		return strings.TrimPrefix(c.Names[0], "/")
	}
	return c.ID[:12]
}

// ListOrphanedContainers returns the names of containers whose project no
// longer exists in the store (read-only preview).
// knownProjectIDs must contain every project ID from the database.
func (m *Manager) ListOrphanedContainers(ctx context.Context, knownProjectIDs map[string]bool) ([]string, error) {
	all, err := m.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}
	var names []string
	for _, c := range all {
		if isOwnedContainer(c, knownProjectIDs) {
			continue
		}
		names = append(names, containerDisplayName(c))
	}
	return names, nil
}

// CleanOrphanedContainers removes all Docker containers whose project no
// longer exists in the store.
// knownProjectIDs must contain every project ID from the database.
func (m *Manager) CleanOrphanedContainers(ctx context.Context, knownProjectIDs map[string]bool) (*CleanupResult, error) {
	all, err := m.cli.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, fmt.Errorf("failed to list containers: %w", err)
	}

	result := newCleanupResult()
	for _, c := range all {
		if isOwnedContainer(c, knownProjectIDs) {
			continue
		}
		name := containerDisplayName(c)
		if err := m.cli.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", name, err))
		} else {
			result.Removed = append(result.Removed, name)
		}
	}
	return result, nil
}

// ownedVolumes returns the set of volume names mounted by containers whose
// project still exists in the store.
func (m *Manager) ownedVolumes(ctx context.Context, knownProjectIDs map[string]bool) map[string]bool {
	vols := map[string]bool{}
	all, _ := m.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "odoo-manager.managed=true")),
	})
	for _, c := range all {
		if !isOwnedContainer(c, knownProjectIDs) {
			continue
		}
		info, err := m.cli.ContainerInspect(ctx, c.ID)
		if err != nil {
			continue
		}
		for _, mount := range info.Mounts {
			if mount.Name != "" {
				vols[mount.Name] = true
			}
		}
	}
	return vols
}

// ListOrphanedVolumes returns the names of volumes not used by any container
// whose project still exists in the store (read-only preview).
func (m *Manager) ListOrphanedVolumes(ctx context.Context, knownProjectIDs map[string]bool) ([]string, error) {
	owned := m.ownedVolumes(ctx, knownProjectIDs)
	volumes, err := m.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}
	var names []string
	for _, v := range volumes.Volumes {
		if !owned[v.Name] {
			names = append(names, v.Name)
		}
	}
	return names, nil
}

// CleanOrphanedVolumes removes all Docker volumes that are NOT used by any
// container whose project still exists in the store.
func (m *Manager) CleanOrphanedVolumes(ctx context.Context, knownProjectIDs map[string]bool) (*CleanupResult, error) {
	owned := m.ownedVolumes(ctx, knownProjectIDs)

	volumes, err := m.cli.VolumeList(ctx, volume.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list volumes: %w", err)
	}

	result := newCleanupResult()
	for _, v := range volumes.Volumes {
		if owned[v.Name] {
			continue
		}
		if err := m.cli.VolumeRemove(ctx, v.Name, true); err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", v.Name, err))
		} else {
			result.Removed = append(result.Removed, v.Name)
		}
	}
	return result, nil
}

// ownedImageIDs returns the set of image IDs used by containers whose
// project still exists in the store.
func (m *Manager) ownedImageIDs(ctx context.Context, knownProjectIDs map[string]bool) map[string]bool {
	ids := map[string]bool{}
	all, _ := m.cli.ContainerList(ctx, container.ListOptions{
		All:     true,
		Filters: filters.NewArgs(filters.Arg("label", "odoo-manager.managed=true")),
	})
	for _, c := range all {
		if isOwnedContainer(c, knownProjectIDs) {
			ids[c.ImageID] = true
		}
	}
	return ids
}

// imageDisplayName returns a human-readable tag or truncated ID for an image.
func imageDisplayName(img image.Summary) string {
	if len(img.RepoTags) > 0 {
		return img.RepoTags[0]
	}
	return img.ID[:19]
}

// ListOrphanedImages returns the tags/IDs of images not used by any container
// whose project still exists in the store (read-only preview).
func (m *Manager) ListOrphanedImages(ctx context.Context, knownProjectIDs map[string]bool) ([]string, error) {
	used := m.ownedImageIDs(ctx, knownProjectIDs)
	images, err := m.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}
	var names []string
	for _, img := range images {
		if used[img.ID] {
			continue
		}
		names = append(names, imageDisplayName(img))
	}
	return names, nil
}

// CleanOrphanedImages removes all Docker images that are NOT used by any
// container whose project still exists in the store.
func (m *Manager) CleanOrphanedImages(ctx context.Context, knownProjectIDs map[string]bool) (*CleanupResult, error) {
	used := m.ownedImageIDs(ctx, knownProjectIDs)

	images, err := m.cli.ImageList(ctx, image.ListOptions{All: false})
	if err != nil {
		return nil, fmt.Errorf("failed to list images: %w", err)
	}

	result := newCleanupResult()
	for _, img := range images {
		if used[img.ID] {
			continue
		}
		tag := imageDisplayName(img)
		_, err := m.cli.ImageRemove(ctx, img.ID, image.RemoveOptions{Force: true, PruneChildren: true})
		if err != nil {
			result.Errors = append(result.Errors, fmt.Sprintf("%s: %v", tag, err))
		} else {
			result.Removed = append(result.Removed, tag)
		}
	}
	return result, nil
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

// ListDatabases runs psql inside the Postgres container and returns the list
// of databases, excluding system databases (postgres, template0, template1).
func (m *Manager) ListDatabases(ctx context.Context, projectID string) ([]string, error) {
	containerName := fmt.Sprintf("postgres-%s", projectID)

	execCfg := container.ExecOptions{
		Cmd:          []string{"psql", "-U", "odoo", "-d", "postgres", "-t", "-A", "-c", "SELECT datname FROM pg_database WHERE datistemplate = false AND datname NOT IN ('postgres') ORDER BY datname"},
		AttachStdout: true,
		AttachStderr: true,
	}

	execResp, err := m.cli.ContainerExecCreate(ctx, containerName, execCfg)
	if err != nil {
		return nil, fmt.Errorf("failed to create exec for listing databases: %w", err)
	}

	attach, err := m.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to attach to exec: %w", err)
	}
	defer attach.Close()

	output, err := io.ReadAll(attach.Reader)
	if err != nil {
		return nil, fmt.Errorf("failed to read exec output: %w", err)
	}

	// Parse the output — each line is a database name.
	// Docker multiplexed streams prepend 8-byte headers per frame.
	raw := string(output)
	var databases []string
	for _, line := range strings.Split(raw, "\n") {
		name := strings.TrimSpace(line)
		// Strip potential Docker stream header bytes (non-printable prefix)
		if idx := strings.IndexFunc(name, func(r rune) bool {
			return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-'
		}); idx > 0 {
			name = name[idx:]
		}
		name = strings.TrimSpace(name)
		if name != "" {
			databases = append(databases, name)
		}
	}

	return databases, nil
}

// BackupDatabase runs "odoo db dump <database>" inside the Odoo container,
// redirecting the zip output to a file inside the container while streaming
// the command's console output (stderr) back to the caller via an io.Reader.
//
// The returned execID can be inspected to check whether the command has
// finished. The caller MUST call the cleanup function when done reading.
func (m *Manager) BackupDatabase(ctx context.Context, projectID, database string) (logReader io.Reader, execID string, cleanup func(), err error) {
	containerName := fmt.Sprintf("odoo-%s", projectID)
	backupPath := "/tmp/odoo_backup.zip"

	// Connect to the linked postgres container (user/password match CreateProject).
	// Redirect stdout (the zip data) to a file; stderr (progress/errors) stays on console.
	cmd := fmt.Sprintf("odoo db --db_host postgres --db_port 5432 --db_user odoo --db_password odoo dump %s > %s", database, backupPath)

	execCfg := container.ExecOptions{
		Cmd:          []string{"sh", "-c", cmd},
		AttachStdout: true,
		AttachStderr: true,
		Tty:          true, // single stream (no multiplexing headers)
	}

	execResp, err := m.cli.ContainerExecCreate(ctx, containerName, execCfg)
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to create exec for backup: %w", err)
	}

	attach, err := m.cli.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{Tty: true})
	if err != nil {
		return nil, "", nil, fmt.Errorf("failed to attach to exec for backup: %w", err)
	}

	cleanup = func() {
		attach.Close()
	}

	return attach.Reader, execResp.ID, cleanup, nil
}

// WaitExec blocks until the given exec process finishes and returns its exit code.
func (m *Manager) WaitExec(ctx context.Context, execID string) (int, error) {
	for {
		inspect, err := m.cli.ContainerExecInspect(ctx, execID)
		if err != nil {
			return -1, err
		}
		if !inspect.Running {
			return inspect.ExitCode, nil
		}
		select {
		case <-ctx.Done():
			return -1, ctx.Err()
		default:
		}
	}
}

// ReadOdooConfig reads /etc/odoo/odoo.conf from the Odoo container.
// Works whether the container is running or stopped.
func (m *Manager) ReadOdooConfig(ctx context.Context, projectID string) (string, error) {
	containerName := fmt.Sprintf("odoo-%s", projectID)
	rc, _, err := m.cli.CopyFromContainer(ctx, containerName, "/etc/odoo/odoo.conf")
	if err != nil {
		return "", fmt.Errorf("failed to read odoo.conf: %w", err)
	}
	defer rc.Close()

	tr := tar.NewReader(rc)
	if _, err := tr.Next(); err != nil {
		return "", fmt.Errorf("failed to read tar header: %w", err)
	}
	data, err := io.ReadAll(tr)
	if err != nil {
		return "", fmt.Errorf("failed to read file contents: %w", err)
	}
	return string(data), nil
}

// WriteOdooConfig writes content to /etc/odoo/odoo.conf inside the Odoo container.
func (m *Manager) WriteOdooConfig(ctx context.Context, projectID string, content string) error {
	containerName := fmt.Sprintf("odoo-%s", projectID)
	return m.writeFileToContainer(ctx, containerName, "/etc/odoo/odoo.conf", []byte(content))
}

// writeFileToContainer writes data into a file inside a container using the
// Docker copy-to-container API (tar archive).
func (m *Manager) writeFileToContainer(ctx context.Context, containerName, filePath string, data []byte) error {
	dir := filepath.Dir(filePath)
	base := filepath.Base(filePath)

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{
		Name: base,
		Mode: 0644,
		Size: int64(len(data)),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return err
	}
	if _, err := tw.Write(data); err != nil {
		return err
	}
	if err := tw.Close(); err != nil {
		return err
	}

	return m.cli.CopyToContainer(ctx, containerName, dir, &buf, container.CopyToContainerOptions{})
}

// CopyBackupFromContainer copies /tmp/odoo_backup.zip out of the Odoo
// container and saves it to destPath on the host. It removes the file
// from the container afterwards.
func (m *Manager) CopyBackupFromContainer(ctx context.Context, projectID, destPath string) error {
	containerName := fmt.Sprintf("odoo-%s", projectID)
	const srcPath = "/tmp/odoo_backup.zip"

	rc, _, err := m.cli.CopyFromContainer(ctx, containerName, srcPath)
	if err != nil {
		return fmt.Errorf("failed to copy backup from container: %w", err)
	}
	defer rc.Close()

	// CopyFromContainer returns a tar archive — extract the single file.
	tr := tar.NewReader(rc)
	_, err = tr.Next()
	if err != nil {
		return fmt.Errorf("failed to read tar header: %w", err)
	}

	// Ensure destination directory exists.
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return err
	}

	out, err := os.Create(destPath)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, tr); err != nil {
		return fmt.Errorf("failed to write backup file: %w", err)
	}

	// Best-effort cleanup inside the container.
	cleanCfg := container.ExecOptions{
		Cmd: []string{"rm", "-f", srcPath},
	}
	if resp, e := m.cli.ContainerExecCreate(ctx, containerName, cleanCfg); e == nil {
		_ = m.cli.ContainerExecStart(ctx, resp.ID, container.ExecStartOptions{})
	}

	return nil
}
