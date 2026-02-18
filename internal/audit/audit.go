package audit

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// Entry represents a single audit log line.
type Entry struct {
	Timestamp string `json:"timestamp"`
	ClientIP  string `json:"client_ip"`
	Method    string `json:"method"`
	Path      string `json:"path"`
	Message   string `json:"message"`
}

// Logger writes audit entries to a log file and broadcasts them to SSE
// subscribers for real-time viewing.
type Logger struct {
	mu       sync.Mutex
	file     *os.File
	filePath string

	subMu   sync.RWMutex
	clients map[chan Entry]struct{}
}

// NewLogger creates a new audit logger that writes to the given file path.
// The file is opened in append mode; it is created if it does not exist.
func NewLogger(path string) (*Logger, error) {
	// Ensure the parent directory exists
	if dir := dirOf(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("audit: mkdir %s: %w", dir, err)
		}
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, fmt.Errorf("audit: open %s: %w", path, err)
	}

	return &Logger{
		file:     f,
		filePath: path,
		clients:  make(map[chan Entry]struct{}),
	}, nil
}

// Log records an audit entry, writes it to the file + stdout, and broadcasts
// it to all SSE subscribers.
func (l *Logger) Log(r *http.Request, message string) {
	entry := Entry{
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		ClientIP:  clientIP(r),
		Method:    r.Method,
		Path:      r.URL.Path,
		Message:   message,
	}

	line := fmt.Sprintf("[%s] %s %s %s — %s", entry.Timestamp, entry.ClientIP, entry.Method, entry.Path, entry.Message)

	// Write to file
	l.mu.Lock()
	fmt.Fprintln(l.file, line)
	l.mu.Unlock()

	// Write to console
	log.Printf("AUDIT: %s", line)

	// Broadcast to SSE subscribers
	l.subMu.RLock()
	for ch := range l.clients {
		select {
		case ch <- entry:
		default:
			// Slow client — drop
		}
	}
	l.subMu.RUnlock()
}

// Subscribe registers a new SSE client for real-time log entries.
func (l *Logger) Subscribe() chan Entry {
	ch := make(chan Entry, 64)
	l.subMu.Lock()
	l.clients[ch] = struct{}{}
	l.subMu.Unlock()
	return ch
}

// Unsubscribe removes a client and closes its channel.
func (l *Logger) Unsubscribe(ch chan Entry) {
	l.subMu.Lock()
	delete(l.clients, ch)
	close(ch)
	l.subMu.Unlock()
}

// Tail reads the last n lines from the audit log file.
// If the file has fewer than n lines, all lines are returned.
// Lines are returned in chronological order (oldest first).
func (l *Logger) Tail(n int) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.Open(l.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()

	return tailLines(f, n)
}

// TailBefore reads up to n lines from the audit log that appear before the
// given offset line (1-indexed from the end). Returns the lines in
// chronological order and the new offset.
func (l *Logger) TailBefore(n int, beforeLine int) (lines []string, newOffset int, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.Open(l.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	defer f.Close()

	// Read all lines (simple approach — audit logs are relatively small)
	all, err := readAllLines(f)
	if err != nil {
		return nil, 0, err
	}

	totalLines := len(all)
	if totalLines == 0 {
		return nil, 0, nil
	}

	// beforeLine is 1-indexed from the end
	// The end index in the slice (exclusive)
	endIdx := totalLines - beforeLine
	if endIdx <= 0 {
		return nil, totalLines, nil
	}

	startIdx := endIdx - n
	if startIdx < 0 {
		startIdx = 0
	}

	return all[startIdx:endIdx], totalLines - startIdx, nil
}

// Close closes the underlying log file.
func (l *Logger) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// clientIP extracts the real client IP from the request, respecting
// X-Forwarded-For (first IP in the chain is the real client).
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		parts := strings.SplitN(xff, ",", 2)
		if ip := strings.TrimSpace(parts[0]); ip != "" {
			return ip
		}
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	// Fall back to RemoteAddr (strip port)
	addr := r.RemoteAddr
	if idx := strings.LastIndex(addr, ":"); idx != -1 {
		return addr[:idx]
	}
	return addr
}

// tailLines reads the last n lines from a reader.
func tailLines(r io.Reader, n int) ([]string, error) {
	scanner := bufio.NewScanner(r)
	ring := make([]string, 0, n)
	for scanner.Scan() {
		ring = append(ring, scanner.Text())
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	return ring, scanner.Err()
}

// readAllLines reads every line from a reader.
func readAllLines(r io.Reader) ([]string, error) {
	scanner := bufio.NewScanner(r)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}

// dirOf returns the directory portion of a file path.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return ""
}
