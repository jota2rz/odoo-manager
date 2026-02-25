package gitops

import (
	"archive/zip"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// resolvedGitBin holds the absolute path to the git executable once resolved.
// Set once at startup by EnsureGit and read by runGit.
var resolvedGitBin string

// localGitPath returns the expected path to a locally-downloaded portable git.
func localGitPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join("bin", "git", "cmd", "git.exe")
	}
	return filepath.Join("bin", "git", "bin", "git")
}

// gitExePath returns the resolved path to the git executable.
func gitExePath() string {
	if resolvedGitBin != "" {
		return resolvedGitBin
	}
	return "git"
}

// EnsureGit makes sure a working git executable is available. It checks:
//  1. The system PATH
//  2. A previously downloaded portable copy in bin/git/
//  3. If neither exists and we're on Windows, downloads MinGit automatically
//
// On non-Windows platforms where git is missing, returns an error with
// install instructions.
func EnsureGit() error {
	// 1. System PATH
	if p, err := exec.LookPath("git"); err == nil {
		resolvedGitBin = p
		logGitVersion()
		return nil
	}

	// 2. Previously downloaded local copy
	localBin := localGitPath()
	if _, err := os.Stat(localBin); err == nil {
		abs, err := filepath.Abs(localBin)
		if err != nil {
			abs = localBin
		}
		resolvedGitBin = abs
		log.Printf("Using local portable git: %s", abs)
		logGitVersion()
		return nil
	}

	// 3. Auto-download (Windows only — MinGit)
	if runtime.GOOS != "windows" {
		return fmt.Errorf("git not found in PATH; install it via your package manager (e.g. apt install git, brew install git)")
	}

	log.Println("git not found — downloading portable MinGit for Windows...")
	if err := downloadMinGit(); err != nil {
		return fmt.Errorf("failed to download portable git: %w", err)
	}

	abs, err := filepath.Abs(localBin)
	if err != nil {
		abs = localBin
	}
	resolvedGitBin = abs
	log.Printf("Portable git ready: %s", abs)
	logGitVersion()
	return nil
}

// logGitVersion prints the installed git version to the log.
func logGitVersion() {
	cmd := exec.Command(gitExePath(), "--version")
	out, err := cmd.Output()
	if err == nil {
		log.Printf("git: %s", strings.TrimSpace(string(out)))
	}
}

// downloadMinGit fetches the latest MinGit portable distribution from
// git-for-windows releases and extracts it to bin/git/.
func downloadMinGit() error {
	var archSuffix string
	switch runtime.GOARCH {
	case "amd64":
		archSuffix = "64-bit"
	case "386":
		archSuffix = "32-bit"
	default:
		return fmt.Errorf("unsupported architecture %s; install git manually from https://git-scm.com/", runtime.GOARCH)
	}

	// Query the latest release from git-for-windows
	log.Println("Querying latest MinGit release...")
	resp, err := http.Get("https://api.github.com/repos/git-for-windows/git/releases/latest")
	if err != nil {
		return fmt.Errorf("failed to query git releases: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API returned status %d; install git manually from https://git-scm.com/", resp.StatusCode)
	}

	var release struct {
		TagName string `json:"tag_name"`
		Assets  []struct {
			Name               string `json:"name"`
			BrowserDownloadURL string `json:"browser_download_url"`
			Size               int64  `json:"size"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return fmt.Errorf("failed to parse release info: %w", err)
	}

	// Find the MinGit zip (non-busybox variant)
	var downloadURL string
	var assetName string
	var assetSize int64
	for _, a := range release.Assets {
		if strings.HasPrefix(a.Name, "MinGit-") &&
			strings.HasSuffix(a.Name, archSuffix+".zip") &&
			!strings.Contains(a.Name, "busybox") {
			downloadURL = a.BrowserDownloadURL
			assetName = a.Name
			assetSize = a.Size
			break
		}
	}

	if downloadURL == "" {
		return fmt.Errorf("could not find MinGit %s asset in release %s; install git manually from https://git-scm.com/", archSuffix, release.TagName)
	}

	log.Printf("Downloading %s (%.1f MB) ...", assetName, float64(assetSize)/(1024*1024))

	// Download to a temp file
	tmpFile, err := os.CreateTemp("", "mingit-*.zip")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	defer os.Remove(tmpPath)

	dlResp, err := http.Get(downloadURL)
	if err != nil {
		tmpFile.Close()
		return fmt.Errorf("download failed: %w", err)
	}
	defer dlResp.Body.Close()

	if dlResp.StatusCode != 200 {
		tmpFile.Close()
		return fmt.Errorf("download returned status %d", dlResp.StatusCode)
	}

	// Copy with progress logging
	pw := &progressWriter{total: assetSize}
	written, err := io.Copy(tmpFile, io.TeeReader(dlResp.Body, pw))
	tmpFile.Close()
	if err != nil {
		return fmt.Errorf("download failed after %d bytes: %w", written, err)
	}
	log.Printf("Download complete (%.1f MB)", float64(written)/(1024*1024))

	// Extract to bin/git/
	destDir := filepath.Join("bin", "git")
	log.Printf("Extracting to %s ...", destDir)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("create git dir: %w", err)
	}
	if err := extractZip(tmpPath, destDir); err != nil {
		return fmt.Errorf("extract failed: %w", err)
	}

	// Verify the binary exists
	if _, err := os.Stat(localGitPath()); err != nil {
		return fmt.Errorf("extraction succeeded but git binary not found at %s", localGitPath())
	}

	log.Println("MinGit installation complete")
	return nil
}

// progressWriter logs download progress every 10%.
type progressWriter struct {
	total      int64
	downloaded int64
	lastPct    int
}

func (pw *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	pw.downloaded += int64(n)
	if pw.total > 0 {
		pct := int(pw.downloaded * 100 / pw.total)
		// Log every 10%
		if pct/10 > pw.lastPct/10 {
			log.Printf("  %d%%", pct)
			pw.lastPct = pct
		}
	}
	return n, nil
}

// extractZip extracts a zip archive to the destination directory.
func extractZip(src, dest string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()

	cleanDest := filepath.Clean(dest)

	for _, f := range r.File {
		target := filepath.Join(dest, f.Name)

		// Prevent zip-slip path traversal
		if !strings.HasPrefix(filepath.Clean(target), cleanDest+string(os.PathSeparator)) {
			continue
		}

		if f.FileInfo().IsDir() {
			os.MkdirAll(target, 0o755)
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}

		outFile, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			return err
		}

		rc, err := f.Open()
		if err != nil {
			outFile.Close()
			return err
		}

		_, copyErr := io.Copy(outFile, rc)
		rc.Close()
		outFile.Close()
		if copyErr != nil {
			return copyErr
		}
	}

	return nil
}
