# 🐳 Odoo Manager

A modern web application for managing Odoo and PostgreSQL Docker containers locally. Built with Go and Templ, featuring a sleek dark theme and a real-time UI powered by Server-Sent Events (SSE).

## Features

- 🚀 **Easy Project Management** - Create, start, stop, and delete Odoo projects with a few clicks
- 🐳 **Docker Integration** - Automatic management of Odoo and PostgreSQL containers
- 📡 **Real-time UI** - Live project status, spinner sync, and log streaming across all browsers via Server-Sent Events (SSE)
- 💾 **Database Backup** - One-click database backup with real-time progress streaming and automatic download
- 📋 **Audit Log** - Full audit trail of all client-to-server events with real-time viewer, file logging, and scroll-back pagination
- 🎨 **Dark Theme UI** - Modern, responsive interface built with Tailwind CSS and Heroicons SVGs
- 📦 **Embedded Frontend** - All assets embedded in a single binary using Templ
- 🗄️ **SQLite Storage** - ACID-compliant project persistence with automatic schema migrations
- 🏷️ **Docker Labels** - Containers are labeled for reliable discovery and management
- 🔄 **Status Reconciliation** - Automatically detects and corrects stale container states
- 🔒 **Idempotent Operations** - Start/stop/delete actions are async, safe to repeat, and sync across browsers
- 🩺 **Docker Health Check** - Continuous monitoring of Docker daemon connectivity with automatic UI overlay
- 🔗 **Connection Recovery** - Automatic SSE reconnection with version-based reload and full-screen overlay
- 🌈 **ANSI Color Support** - Terminal colors rendered faithfully in log viewers
- 🌐 **Reverse Proxy Aware** - Client IP detection via `X-Forwarded-For` and `X-Real-Ip` headers
- ⚡ **Fast & Lightweight** - Minimal dependencies, quick startup

## Prerequisites

- **Docker** (with Docker daemon running)

To build from source you also need:

- **Go** 1.24 or higher
- **Git** (for cloning the repository)

## Installation

### Download a Pre-built Binary

Download the latest release for your platform from the [GitHub Releases](https://github.com/jota2rz/odoo-manager/releases) page.

Available platforms:
| OS | Architecture | File |
|---|---|---|
| Linux | amd64 | `odoo-manager_*_linux_amd64.tar.gz` |
| Linux | arm64 | `odoo-manager_*_linux_arm64.tar.gz` |
| macOS | amd64 (Intel) | `odoo-manager_*_darwin_amd64.tar.gz` |
| macOS | arm64 (Apple Silicon) | `odoo-manager_*_darwin_arm64.tar.gz` |
| Windows | amd64 | `odoo-manager_*_windows_amd64.zip` |

Extract and run:

```bash
# Linux/macOS
tar xzf odoo-manager_*.tar.gz
./odoo-manager

# Windows
# Extract the zip, then run odoo-manager.exe
```

### Build from Source

#### 1. Clone the repository

```bash
git clone https://github.com/jota2rz/odoo-manager.git
cd odoo-manager
```

#### 2. Initialize the project

```bash
make init
```

This will:
- Install required tools (Templ)
- Download Go dependencies
- Generate Templ templates

#### 3. Build the application

```bash
make build
```

#### 4. Run the application

```bash
make run
```

Or simply run the binary directly:

```bash
./odoo-manager
```

The application will start on `http://localhost:8080`

## Usage

### Creating a Project

1. Click the **"+ New Project"** button
2. Fill in the project details:
   - **Name**: Your project name
   - **Description**: Optional project description
   - **Odoo Version**: Select from 15.0, 16.0, 17.0, 18.0, 19.0
   - **PostgreSQL Version**: Select from 14, 15, 16, 17, 18
   - **Port**: Port to expose Odoo (default: 8069)
3. Click **"Create"** — containers are automatically pulled and created in the background

### Managing Projects

- **Start**: Click the green "Start" button to launch containers
- **Stop**: Click the red "Stop" button to stop running containers
- **Open**: Click "Open" to access the running Odoo instance (visible only when running)
- **Backup**: Click the database icon to back up a database (visible only when running)
- **View Logs**: Click the document icon to stream real-time container logs
- **Delete**: Click the trash icon to remove the project and its containers

All actions are asynchronous and reflected in real time across every open browser tab via SSE. Start, stop, and delete operations return immediately while Docker work runs in the background.

### Viewing Logs

1. Click the document icon on any project
2. Select the container (Odoo or PostgreSQL) from the dropdown
3. View real-time logs with full ANSI terminal color rendering
4. Logs automatically scroll to show the latest entries

### Database Backup

1. Click the database icon on a running project
2. If the project has multiple databases, a picker modal appears — select one
3. A log modal streams real-time backup progress from the container
4. Once complete, the backup `.zip` file downloads automatically
5. Only one backup per project can run at a time (enforced across all browsers)

### Audit Log

1. Click **"Audit"** in the navigation bar
2. View all client-to-server API events in real time
3. Each entry shows timestamp, client IP, HTTP method, path, and description
4. Scroll up to load older log entries (100 lines per page)
5. Audit entries are also written to `data/audit.log` and the server console

## Development

### Project Structure

```
odoo-manager/
├── cmd/
│   └── odoo-manager/        # Main application entry point
│       ├── main.go
│       └── static/           # Embedded frontend assets
│           ├── css/
│           │   └── style.css # Generated by Tailwind CLI (git-ignored)
│           └── js/
│               └── app.js    # SSE client, card rendering, API actions
├── internal/
│   ├── audit/               # Audit logging (file + console + SSE)
│   │   └── audit.go
│   ├── docker/              # Docker container lifecycle & backup
│   │   └── docker.go
│   ├── events/              # SSE event hub (pub/sub)
│   │   └── events.go
│   ├── handlers/            # HTTP handlers, routes, and SSE endpoint
│   │   └── handlers.go
│   └── store/               # SQLite persistence and migrations
│       ├── store.go
│       └── migrations.go
├── src/
│   └── css/
│       └── input.css        # Tailwind CSS source
├── templates/               # Templ HTML templates
│   └── templates.templ
├── data/                    # Runtime data (odoo-manager.db, audit.log, backups/)
├── .goreleaser.yml          # GoReleaser configuration
├── .github/
│   └── workflows/
│       └── release.yml      # GitHub Actions release workflow
├── .vscode/                 # VS Code debug & task configuration
├── Makefile                 # Build automation
├── go.mod                   # Go module file
└── README.md
```

### Available Commands

```bash
make help       # Show all available commands
make init       # First-time setup (install tools, deps, templ, tailwind)
make install    # Install development tools (Templ, Tailwind CLI)
make deps       # Download Go dependencies
make templ      # Generate Templ templates
make tailwind   # Build Tailwind CSS
make build      # Build the application (templ + tailwind + go build)
make run        # Build and run
make dev        # Run with auto-reload (requires air)
make clean      # Clean build artifacts
make test       # Run tests
make fmt        # Format code (Go + Templ)
make lint       # Lint code (golangci-lint)
```

### Development Mode

For development with auto-reload:

```bash
make dev
```

This uses [Air](https://github.com/cosmtrek/air) for live reloading during development.

### Modifying Templates

Templates are written in [Templ](https://templ.guide/). After editing `.templ` files:

```bash
make templ  # Regenerate Go code from templates
make build  # Rebuild the application
```

## Configuration

### Environment Variables

- `PORT` - Server port (default: 8080)

Example:
```bash
PORT=3000 ./odoo-manager
```

### Data Persistence

Projects are stored in a SQLite database at `data/odoo-manager.db`. The database is created automatically on first run with WAL mode enabled for better concurrent read performance. Schema changes are applied automatically via versioned migrations (`PRAGMA user_version`). Unique constraints on project names and ports prevent duplicates. No external database server is required — everything is embedded in the single binary.

Audit entries are appended to `data/audit.log` in a human-readable format. Database backups are temporarily stored in `data/backups/` and cleaned up after download.

## Docker Integration

The application uses the Docker SDK for Go to manage containers. Ensure Docker is running before starting the application.

### Container Naming Convention

- Odoo containers: `odoo-{project-id}`
- PostgreSQL containers: `postgres-{project-id}`

### Container Labels

All managed containers are tagged with the following Docker labels for reliable discovery:

| Label | Description |
|---|---|
| `odoo-manager.project-id` | The project's unique identifier |
| `odoo-manager.role` | Container role (`odoo` or `postgres`) |
| `odoo-manager.managed` | Always `true` — marks containers as managed |

You can query managed containers with:

```bash
docker ps --filter label=odoo-manager.managed=true
```

### Default Container Configuration

**PostgreSQL:**
- Image: `postgres:{version}` (Odoo ≤ 18), `pgvector/pgvector:pg{version}-trixie` (Odoo ≥ 19)
- Database: `postgres`
- User: `odoo`
- Password: `odoo`

**Odoo:**
- Image: `odoo:{version}`
- Port: Configurable per project
- Linked to PostgreSQL container

## Troubleshooting

### Docker Connection Issues

If you see "Docker manager not available":
- Ensure Docker is running: `docker ps`
- Check Docker socket permissions
- On Linux: Add your user to the docker group

### Port Already in Use

If a port is already in use:
- Choose a different port when creating the project
- Stop any conflicting services
- Check running containers: `docker ps`

### Build Errors

If you encounter build errors:
```bash
make clean      # Clean old artifacts
make deps       # Refresh dependencies
make build      # Rebuild
```

## Architecture

### Backend (Go)

- **HTTP Server**: Standard library `net/http`
- **Docker SDK**: Official Docker client for Go
- **Templating**: Templ for type-safe HTML templates
- **Storage**: SQLite via [modernc.org/sqlite](https://pkg.go.dev/modernc.org/sqlite) (pure Go, no CGo)

### Frontend

- **Tailwind CSS v4**: Utility-first CSS via standalone CLI (no Node.js required)
- **SSE**: Real-time UI updates and log streaming across all connected browsers

### Key Features

1. **Single Binary Deployment**: All assets embedded using Go's embed
2. **Embedded SQLite**: ACID-compliant storage with automatic schema migrations and unique constraints
3. **Real-time UI**: SSE broadcasts project status changes, pending actions, backup progress, and log streams to all connected browsers instantly
4. **Docker Native**: Direct Docker API integration with container labels and health monitoring
5. **Auto-provisioning**: Containers are pulled and created in the background as soon as a project is created
6. **Async Operations**: Start, stop, and delete run in background goroutines to avoid HTTP timeouts
7. **Database Backup**: Runs `odoo db dump` inside the container, streams progress via SSE, and copies the backup file out
8. **Audit Trail**: Every API request is logged to file, console, and streamed live to the Audit page with client IP tracking
9. **Connection Resilience**: SSE auto-reconnect with version-based reload, connection-lost overlay, and Docker-down overlay
10. **ANSI Color Rendering**: Full terminal color support in log and backup viewers via client-side conversion
11. **Status Reconciliation**: Automatically corrects stale container states
12. **Graceful Shutdown**: Proper signal handling
13. **Cross-platform Releases**: Automated builds via GoReleaser + GitHub Actions

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

1. Fork the repository
2. Create your feature branch (`git checkout -b feature/amazing-feature`)
3. Commit your changes (`git commit -m 'Add some amazing feature'`)
4. Push to the branch (`git push origin feature/amazing-feature`)
5. Open a Pull Request

## Releasing

Releases are automated with [GoReleaser](https://goreleaser.com/) and GitHub Actions. To publish a new release:

```bash
git tag v1.0.0
git push origin v1.0.0
```

This triggers the release workflow, which cross-compiles for all supported platforms and publishes binaries with checksums to GitHub Releases.

## License

This project is open source and available under the MIT License.

## Support

For issues, questions, or contributions, please visit the [GitHub repository](https://github.com/jota2rz/odoo-manager).

## Acknowledgments

- [Odoo](https://www.odoo.com/) - Open source ERP and CRM
- [Docker](https://www.docker.com/) - Container platform
- [Templ](https://templ.guide/) - Type-safe templating

---

**Made with ❤️ for the Odoo community**