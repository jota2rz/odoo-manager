package gitops

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing/transport"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

// ValidateRepoURL checks that the URL is a valid https://*.git URL format.
func ValidateRepoURL(url string) error {
	if !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("URL must start with https://")
	}
	if !strings.HasSuffix(url, ".git") {
		return fmt.Errorf("URL must end with .git")
	}
	return nil
}

// CheckRepoAccessible verifies that the remote repository exists and is
// reachable. Uses the PAT token for authentication if provided.
func CheckRepoAccessible(ctx context.Context, repoURL, token string) error {
	if err := ValidateRepoURL(repoURL); err != nil {
		return err
	}

	// Build a transport endpoint to use go-git's ls-remote equivalent.
	ep, err := transport.NewEndpoint(repoURL)
	if err != nil {
		return fmt.Errorf("invalid repository URL: %w", err)
	}

	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{
			Username: "x-access-token", // GitHub PAT convention
			Password: token,
		}
	}

	client, err := githttp.DefaultClient.NewUploadPackSession(ep, auth)
	if err != nil {
		return fmt.Errorf("cannot connect to repository: %w", err)
	}
	defer client.Close()

	info, err := client.AdvertisedReferencesContext(ctx)
	if err != nil {
		return fmt.Errorf("repository not accessible: %w", err)
	}
	if info == nil {
		return fmt.Errorf("repository returned no references")
	}

	return nil
}

// repoDir returns the local directory where a project's repo is cloned.
func repoDir(projectID string) string {
	return filepath.Join("data", "repos", projectID)
}

// enterpriseRepoDir returns the local directory where a project's enterprise repo is cloned.
func enterpriseRepoDir(projectID string) string {
	return filepath.Join("data", "repos", projectID+"-enterprise")
}

// designThemesRepoDir returns the local directory where a project's design-themes repo is cloned.
func designThemesRepoDir(projectID string) string {
	return filepath.Join("data", "repos", projectID+"-design-themes")
}

// EnterpriseRepoURL is the fixed URL for the Odoo Enterprise repository.
const EnterpriseRepoURL = "https://github.com/odoo/enterprise.git"

// DesignThemesRepoURL is the fixed URL for the Odoo Design Themes repository.
const DesignThemesRepoURL = "https://github.com/odoo/design-themes.git"

// CloneOrPull clones the repository if it doesn't exist locally, or pulls
// the latest changes if it does. When branch is non-empty the specific
// branch is checked out. Returns the local directory path.
// Uses native git CLI for performance with large repos.
func CloneOrPull(ctx context.Context, projectID, repoURL, token, branch string) (string, error) {
	dir := repoDir(projectID)
	authURL := injectToken(repoURL, token)

	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", fmt.Errorf("create repo parent dir: %w", err)
		}
		args := []string{"clone", "--progress"}
		if branch != "" {
			args = append(args, "--branch", branch, "--single-branch")
		}
		args = append(args, authURL, dir)
		log.Printf("gitops: cloning %s into %s ...", repoURL, dir)
		if err := runGit(ctx, "", args...); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("clone failed: %w", err)
		}
		log.Printf("gitops: clone complete for %s", repoURL)
	} else {
		log.Printf("gitops: pulling latest for %s ...", repoURL)
		pullArgs := []string{"pull", "--force"}
		if branch != "" {
			pullArgs = append(pullArgs, "origin", branch)
		}
		if err := runGit(ctx, dir, pullArgs...); err != nil {
			return "", fmt.Errorf("pull failed: %w", err)
		}
		log.Printf("gitops: pull complete for %s", repoURL)
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil
	}
	return abs, nil
}

// RemoveRepo deletes the local clone for a project.
func RemoveRepo(projectID string) error {
	dir := repoDir(projectID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// CloneOrPullEnterprise clones or pulls the Odoo Enterprise repository for a
// project. Uses the same branch as the project's Odoo version. Returns the
// local directory path. Uses native git CLI for performance.
func CloneOrPullEnterprise(ctx context.Context, projectID, token, branch string) (string, error) {
	dir := enterpriseRepoDir(projectID)
	authURL := injectToken(EnterpriseRepoURL, token)

	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", fmt.Errorf("create enterprise repo parent dir: %w", err)
		}
		args := []string{"clone", "--progress", "--depth", "1"}
		if branch != "" {
			args = append(args, "--branch", branch, "--single-branch")
		}
		args = append(args, authURL, dir)
		log.Printf("gitops: cloning enterprise repo into %s ...", dir)
		if err := runGit(ctx, "", args...); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("clone enterprise failed: %w", err)
		}
		log.Printf("gitops: enterprise clone complete")
	} else {
		log.Printf("gitops: pulling latest enterprise ...")
		pullArgs := []string{"pull", "--force"}
		if branch != "" {
			pullArgs = append(pullArgs, "origin", branch)
		}
		if err := runGit(ctx, dir, pullArgs...); err != nil {
			return "", fmt.Errorf("enterprise pull failed: %w", err)
		}
		log.Printf("gitops: enterprise pull complete")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil
	}
	return abs, nil
}

// RemoveEnterpriseRepo deletes the local enterprise clone for a project.
func RemoveEnterpriseRepo(projectID string) error {
	dir := enterpriseRepoDir(projectID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// CheckEnterpriseAccess verifies that the PAT token has access to the Odoo
// Enterprise repository. Returns nil if accessible, error otherwise.
func CheckEnterpriseAccess(ctx context.Context, token string) error {
	return CheckRepoAccessible(ctx, EnterpriseRepoURL, token)
}

// CloneOrPullDesignThemes clones or pulls the Odoo Design Themes repository
// for a project. Uses the same branch as the project's Odoo version. Returns
// the local directory path. Uses native git CLI for performance.
func CloneOrPullDesignThemes(ctx context.Context, projectID, token, branch string) (string, error) {
	dir := designThemesRepoDir(projectID)
	authURL := injectToken(DesignThemesRepoURL, token)

	if _, err := os.Stat(filepath.Join(dir, ".git")); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
			return "", fmt.Errorf("create design-themes repo parent dir: %w", err)
		}
		args := []string{"clone", "--progress", "--depth", "1"}
		if branch != "" {
			args = append(args, "--branch", branch, "--single-branch")
		}
		args = append(args, authURL, dir)
		log.Printf("gitops: cloning design-themes repo into %s ...", dir)
		if err := runGit(ctx, "", args...); err != nil {
			os.RemoveAll(dir)
			return "", fmt.Errorf("clone design-themes failed: %w", err)
		}
		log.Printf("gitops: design-themes clone complete")
	} else {
		log.Printf("gitops: pulling latest design-themes ...")
		pullArgs := []string{"pull", "--force"}
		if branch != "" {
			pullArgs = append(pullArgs, "origin", branch)
		}
		if err := runGit(ctx, dir, pullArgs...); err != nil {
			return "", fmt.Errorf("design-themes pull failed: %w", err)
		}
		log.Printf("gitops: design-themes pull complete")
	}

	abs, err := filepath.Abs(dir)
	if err != nil {
		return dir, nil
	}
	return abs, nil
}

// RemoveDesignThemesRepo deletes the local design-themes clone for a project.
func RemoveDesignThemesRepo(projectID string) error {
	dir := designThemesRepoDir(projectID)
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		return nil
	}
	return os.RemoveAll(dir)
}

// CheckDesignThemesAccess verifies that the PAT token has access to the Odoo
// Design Themes repository. Returns nil if accessible, error otherwise.
func CheckDesignThemesAccess(ctx context.Context, token string) error {
	return CheckRepoAccessible(ctx, DesignThemesRepoURL, token)
}

// runGit executes a native git command with the given arguments. If workDir
// is non-empty it is used as the working directory. Stdout and stderr are
// sent to the process logger so the user can see clone/pull progress.
func runGit(ctx context.Context, workDir string, args ...string) error {
	cmd := exec.CommandContext(ctx, gitExePath(), args...)
	if workDir != "" {
		cmd.Dir = workDir
	}
	cmd.Stdout = os.Stderr // git progress goes to stderr of the server
	cmd.Stderr = os.Stderr
	// Prevent git from asking for credentials interactively
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	return cmd.Run()
}

// injectToken returns a repo URL with a PAT token embedded for HTTPS auth.
// If token is empty the original URL is returned unchanged.
func injectToken(repoURL, token string) string {
	if token == "" {
		return repoURL
	}
	// https://github.com/... → https://x-access-token:TOKEN@github.com/...
	return strings.Replace(repoURL, "https://", "https://x-access-token:"+token+"@", 1)
}

// ListBranches returns the branch names available on the remote repository,
// sorted alphabetically. Uses go-git's ls-remote equivalent.
func ListBranches(ctx context.Context, repoURL, token string) ([]string, error) {
	rem := git.NewRemote(nil, &config.RemoteConfig{
		Name: "origin",
		URLs: []string{repoURL},
	})

	var auth transport.AuthMethod
	if token != "" {
		auth = &githttp.BasicAuth{
			Username: "x-access-token",
			Password: token,
		}
	}

	refs, err := rem.ListContext(ctx, &git.ListOptions{Auth: auth})
	if err != nil {
		return nil, fmt.Errorf("list remote refs: %w", err)
	}

	var branches []string
	for _, ref := range refs {
		if ref.Name().IsBranch() {
			branches = append(branches, ref.Name().Short())
		}
	}

	sort.Strings(branches)
	return branches, nil
}

// ValidateToken makes a lightweight GitHub API call to verify a PAT token is valid.
func ValidateToken(ctx context.Context, token string) error {
	if token == "" {
		return fmt.Errorf("token is empty")
	}

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.github.com/user", nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("failed to reach GitHub API: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 401 {
		return fmt.Errorf("invalid or expired token")
	}
	if resp.StatusCode != 200 {
		return fmt.Errorf("GitHub API returned status %d", resp.StatusCode)
	}
	return nil
}
