package scanner

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/go-git/go-git/v5"
)

type WorkspaceType string

const (
	TypePlain   WorkspaceType = "plain"
	TypeGitRepo WorkspaceType = "git_repo"
	TypeWorktree WorkspaceType = "worktree"
)

type Workspace struct {
	Path      string
	Name      string
	Type      WorkspaceType
	Branch    string
	Worktrees []WorktreeInfo
	// IsBare is true for the <repo>/.bare container layout, where Path itself
	// holds no working tree — only its Worktrees are actually checked out.
	IsBare bool
	// set after reconcile with state
	TmuxSession string
	HasSession  bool
}

type WorktreeInfo struct {
	Path        string
	Branch      string
	TmuxSession string
	HasSession  bool
}

// Rescan re-detects a single workspace at path, refreshing its branch and
// worktree list after an external change (e.g. a newly created worktree).
func Rescan(path string) Workspace {
	return detectWorkspace(path)
}

var worktreePathUnsafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func sanitizePathSegment(s string) string {
	return strings.Trim(worktreePathUnsafe.ReplaceAllString(s, "-"), "-")
}

// bareGitDir returns path's ".bare" subdirectory if it holds a bare git repo,
// used for the <repo>/.bare + <repo>/<branch> worktree layout.
func bareGitDir(path string) (string, bool) {
	dir := filepath.Join(path, ".bare")
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--is-bare-repository").Output()
	if err != nil || strings.TrimSpace(string(out)) != "true" {
		return "", false
	}
	return dir, true
}

// AddWorktree creates a new git worktree for branch under repoPath. If branch
// already exists locally, it's checked out as-is. Else if it exists on the
// "origin" remote (already fetched — this never touches the network), a new
// local branch is created tracking it. Otherwise a new branch is created off
// HEAD. If repoPath uses the <repo>/.bare layout, the worktree is nested at
// <repoPath>/<branch>; otherwise it's a sibling directory named
// <repo>-<branch>. It returns the new worktree's path.
func AddWorktree(repoPath, branch string) (string, error) {
	branch = strings.TrimSpace(branch)
	if branch == "" {
		return "", fmt.Errorf("branch name required")
	}

	gitDir := repoPath
	var dest string
	if dir, ok := bareGitDir(repoPath); ok {
		gitDir = dir
		dest = filepath.Join(repoPath, sanitizePathSegment(branch))
	} else {
		dest = filepath.Join(filepath.Dir(repoPath), filepath.Base(repoPath)+"-"+sanitizePathSegment(branch))
	}

	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("%s already exists", dest)
	}

	var args []string
	switch {
	case refExists(gitDir, "refs/heads/"+branch):
		args = []string{"-C", gitDir, "worktree", "add", dest, branch}
	case refExists(gitDir, "refs/remotes/origin/"+branch):
		args = []string{"-C", gitDir, "worktree", "add", "--track", "-b", branch, dest, "origin/" + branch}
	default:
		args = []string{"-C", gitDir, "worktree", "add", "-b", branch, dest}
	}

	out, err := exec.Command("git", args...).CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git worktree add: %s", cleanGitOutput(out, err))
	}
	return dest, nil
}

// RemoveWorktree removes the worktree at worktreePath from repoPath (its
// .bare dir if bare-structured, otherwise the repo itself). git refuses this
// if the worktree has uncommitted or untracked changes, which surfaces here
// as an error rather than being force-removed.
func RemoveWorktree(repoPath, worktreePath string) error {
	gitDir := repoPath
	if dir, ok := bareGitDir(repoPath); ok {
		gitDir = dir
	}

	out, err := exec.Command("git", "-C", gitDir, "worktree", "remove", worktreePath).CombinedOutput()
	if err != nil {
		return fmt.Errorf("git worktree remove: %s", cleanGitOutput(out, err))
	}
	return nil
}

func refExists(gitDir, ref string) bool {
	cmd := exec.Command("git", "-C", gitDir, "show-ref", "--verify", "--quiet", ref)
	return cmd.Run() == nil
}

// Fetch runs `git fetch origin` for repoPath (its .bare dir if bare-structured,
// otherwise the repo itself), updating remote-tracking refs so branches
// pushed elsewhere (CI, dependabot, teammates) become visible to AddWorktree.
func Fetch(repoPath string) error {
	gitDir := repoPath
	if dir, ok := bareGitDir(repoPath); ok {
		gitDir = dir
	}

	out, err := exec.Command("git", "-C", gitDir, "fetch", "origin").CombinedOutput()
	if err != nil {
		return fmt.Errorf("git fetch: %s", cleanGitOutput(out, err))
	}
	return nil
}

// PRInfo is a single open pull request, as shown in the Ctrl+R picker.
type PRInfo struct {
	Number int
	Title  string
	Author string
}

// remoteSlug derives "owner/repo" from the origin remote URL, so gh commands
// can target the right repo explicitly rather than relying on gh's own
// cwd-based detection (which is untested against the .bare layout).
func remoteSlug(gitDir string) (string, error) {
	out, err := exec.Command("git", "-C", gitDir, "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("git remote get-url origin: %w", err)
	}
	url := strings.TrimSuffix(strings.TrimSpace(string(out)), "/")
	url = strings.TrimSuffix(url, ".git")
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return "", fmt.Errorf("can't parse remote url %q", url)
	}
	return parts[len(parts)-2] + "/" + parts[len(parts)-1], nil
}

// ListOpenPRs fetches open pull requests for repoPath's origin remote via the
// gh CLI. Like CloneBare/Fetch, this is an explicit user-triggered network
// call (see AGENTS.md's "no network calls" exceptions).
func ListOpenPRs(repoPath string) ([]PRInfo, error) {
	gitDir := repoPath
	if dir, ok := bareGitDir(repoPath); ok {
		gitDir = dir
	}

	slug, err := remoteSlug(gitDir)
	if err != nil {
		return nil, err
	}

	out, err := exec.Command("gh", "pr", "list", "--repo", slug, "--json", "number,title,author", "--limit", "50").Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}

	var raw []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Author struct {
			Login string `json:"login"`
		} `json:"author"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("parse gh pr list output: %w", err)
	}

	prs := make([]PRInfo, len(raw))
	for i, r := range raw {
		prs[i] = PRInfo{Number: r.Number, Title: r.Title, Author: r.Author.Login}
	}
	return prs, nil
}

// FetchPR fetches PR number n's head ref from origin into a local branch
// named "pr-<n>", so AddWorktree can create a worktree for it exactly like
// any other branch — it just sees a ref under refs/heads/.
func FetchPR(repoPath string, n int) (branch string, err error) {
	gitDir := repoPath
	if dir, ok := bareGitDir(repoPath); ok {
		gitDir = dir
	}

	branch = fmt.Sprintf("pr-%d", n)
	refspec := fmt.Sprintf("pull/%d/head:%s", n, branch)
	out, err := exec.Command("git", "-C", gitDir, "fetch", "origin", refspec, "--force").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git fetch %s: %s", refspec, cleanGitOutput(out, err))
	}
	return branch, nil
}

func cleanGitOutput(out []byte, err error) string {
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return msg
}

// ResolveCloneSource turns a user-provided clone target into a git clone URL,
// a repo name, and the owner (used to pick which scan_path to clone into —
// see CloneBare). Accepted forms: "owner/repo" (uses defaultHost),
// "host/owner/repo", or a full "git@..."/"https://..." URL. protocol controls
// the URL built for the shorthand forms ("https" or "ssh"); it's ignored for
// forms that already spell out a full URL.
func ResolveCloneSource(input, defaultHost, protocol string) (url string, name string, owner string, err error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return "", "", "", fmt.Errorf("repo required")
	}
	if defaultHost == "" {
		defaultHost = "github.com"
	}
	if protocol == "" {
		protocol = "https"
	}

	if strings.Contains(input, "://") || strings.HasPrefix(input, "git@") {
		name, owner = repoNameAndOwnerFromURL(input)
		return input, name, owner, nil
	}

	trimmed := strings.TrimSuffix(strings.TrimSuffix(input, "/"), ".git")
	parts := strings.Split(trimmed, "/")

	var host, repo string
	switch len(parts) {
	case 2:
		host, owner, repo = defaultHost, parts[0], parts[1]
	case 3:
		host, owner, repo = parts[0], parts[1], parts[2]
	default:
		return "", "", "", fmt.Errorf("can't parse %q — expected owner/repo, host/owner/repo, or a full git URL", input)
	}
	if owner == "" || repo == "" {
		return "", "", "", fmt.Errorf("can't parse %q — expected owner/repo, host/owner/repo, or a full git URL", input)
	}

	if protocol == "ssh" {
		return fmt.Sprintf("git@%s:%s/%s.git", host, owner, repo), repo, owner, nil
	}
	return fmt.Sprintf("https://%s/%s/%s.git", host, owner, repo), repo, owner, nil
}

// repoNameAndOwnerFromURL extracts the repo name and owner from a full
// "git@host:owner/repo.git" or "https://host/owner/repo.git" URL.
func repoNameAndOwnerFromURL(url string) (name string, owner string) {
	url = strings.TrimSuffix(strings.TrimSuffix(url, "/"), ".git")
	// Split on both '/' and ':' so "git@host:owner/repo" and
	// "https://host/owner/repo" are handled the same way.
	segs := strings.FieldsFunc(url, func(r rune) bool { return r == '/' || r == ':' })
	switch len(segs) {
	case 0:
		return "", ""
	case 1:
		return segs[0], ""
	default:
		return segs[len(segs)-1], segs[len(segs)-2]
	}
}

// CloneBare clones url as a bare repo into <scanRoot>/<name>/.bare and fixes
// up the origin fetch refspec so `git fetch` behaves normally afterward
// (plain `git clone --bare` doesn't wire this up). It returns the new repo's
// container path and the remote's default branch.
func CloneBare(scanRoot, name, url string) (repoPath string, defaultBranch string, err error) {
	repoPath = filepath.Join(scanRoot, name)
	bareDir := filepath.Join(repoPath, ".bare")
	if _, err := os.Stat(repoPath); err == nil {
		return "", "", fmt.Errorf("%s already exists", repoPath)
	}

	if err := os.MkdirAll(repoPath, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir %s: %w", repoPath, err)
	}

	if out, err := exec.Command("git", "clone", "--bare", url, bareDir).CombinedOutput(); err != nil {
		os.RemoveAll(repoPath)
		return "", "", fmt.Errorf("git clone: %s", cleanGitOutput(out, err))
	}

	refspec := "+refs/heads/*:refs/remotes/origin/*"
	if out, err := exec.Command("git", "-C", bareDir, "config", "remote.origin.fetch", refspec).CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git config remote.origin.fetch: %s", cleanGitOutput(out, err))
	}
	if out, err := exec.Command("git", "-C", bareDir, "fetch", "origin").CombinedOutput(); err != nil {
		return "", "", fmt.Errorf("git fetch: %s", cleanGitOutput(out, err))
	}

	branch, err := defaultRemoteBranch(url)
	if err != nil || branch == "" {
		branch = "main"
	}

	return repoPath, branch, nil
}

func defaultRemoteBranch(url string) (string, error) {
	out, err := exec.Command("git", "ls-remote", "--symref", url, "HEAD").Output()
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if !strings.HasPrefix(line, "ref: ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "ref: "))
		if len(fields) > 0 {
			return strings.TrimPrefix(fields[0], "refs/heads/"), nil
		}
	}
	return "", fmt.Errorf("could not determine default branch")
}

func detectWorkspace(path string) Workspace {
	ws := Workspace{
		Path: path,
		Name: filepath.Base(path),
		Type: TypePlain,
	}

	if bareDir, ok := bareGitDir(path); ok {
		ws.Type = TypeGitRepo
		ws.IsBare = true
		ws.Worktrees = listWorktrees(bareDir)
		return ws
	}

	repo, err := git.PlainOpen(path)
	if err != nil {
		// Try detecting if it's inside a worktree by walking up
		return ws
	}

	ws.Type = TypeGitRepo

	// Get current branch
	if head, err := repo.Head(); err == nil {
		if head.Name().IsBranch() {
			ws.Branch = head.Name().Short()
		} else {
			ws.Branch = head.Hash().String()[:7]
		}
	}

	// Use git CLI for worktree listing (more reliable than go-git for linked worktrees)
	ws.Worktrees = listWorktrees(path)

	return ws
}

func listWorktrees(repoPath string) []WorktreeInfo {
	cmd := exec.Command("git", "-C", repoPath, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var worktrees []WorktreeInfo
	var current WorktreeInfo
	var currentIsBare bool

	flush := func() {
		// The "bare" entry is the .bare admin directory itself, not a real
		// worktree — git worktree list always includes it for bare repos.
		if current.Path != "" && !currentIsBare {
			worktrees = append(worktrees, current)
		}
	}

	for _, line := range strings.Split(string(bytes.TrimSpace(out)), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			flush()
			current = WorktreeInfo{Path: strings.TrimPrefix(line, "worktree ")}
			currentIsBare = false
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "detached":
			current.Branch = "HEAD (detached)"
		case line == "bare":
			currentIsBare = true
		case line == "":
			// blank line separates entries
		}
	}
	flush()

	return worktrees
}
