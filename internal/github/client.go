// Package github implements the GitHubPRClient that opens automated healing
// pull requests on behalf of GopherGuard.
//
// Flow per healing cycle:
//  1. Find the Deployment manifest file in the repo (conventional path).
//  2. Fetch its current content via the GitHub Contents API.
//  3. Apply the LLM-generated strategic merge patch with ApplyYAMLPatch.
//  4. Create a new branch  gopherguard/fix-<deployment>-<unix-ts>.
//  5. Commit the patched file to that branch.
//  6. Open a pull request with structured body (root cause + witty line + patch).
package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	gogithub "github.com/google/go-github/v62/github"
	"golang.org/x/oauth2"

	"github.com/tonyjoanes/gopher-guard/internal/llm"
)

const (
	// MaxHealingAttempts caps how many PRs GopherGuard will open for the same
	// deployment before giving up (prevents infinite auto-PR loops).
	MaxHealingAttempts = 5

	// conventionalPaths lists candidate deployment file paths tried in order.
	// The first one found in the repo is used.
	// Users can add their own layout by PRing this slice.
)

var conventionalPaths = []string{
	"deploy/%s/deployment.yaml",
	"deploy/%s/deployment.yml",
	"manifests/%s/deployment.yaml",
	"manifests/%s.yaml",
	"k8s/%s/deployment.yaml",
}

// PRRequest carries everything needed to open one healing PR.
type PRRequest struct {
	// Owner and Repo identify the GitHub repository (from spec.gitRepo "owner/repo").
	Owner string
	Repo  string
	// DeploymentName is the target Deployment (used to find the manifest file).
	DeploymentName string
	// Namespace of the Deployment (used in PR body for context).
	Namespace string
	// Diagnosis is the LLM output to embed in the PR.
	Diagnosis *llm.Diagnosis
	// HealingScore is the current score, used in the PR body.
	HealingScore int32
}

// PRResult is returned after a successful PR creation.
type PRResult struct {
	PRURL      string
	BranchName string
	FilePath   string
}

// GitHubPRClient uses the GitHub REST API to open healing pull requests.
type GitHubPRClient struct {
	gh *gogithub.Client
}

// NewGitHubPRClient creates an authenticated GitHub client using a personal
// access token or GitHub App installation token.
func NewGitHubPRClient(token string) *GitHubPRClient {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: token})
	tc := oauth2.NewClient(context.Background(), ts)
	return &GitHubPRClient{gh: gogithub.NewClient(tc)}
}

// CreateHealingPR executes the full find → patch → branch → commit → PR flow.
// It returns the PR URL and the branch name on success.
func (c *GitHubPRClient) CreateHealingPR(ctx context.Context, req PRRequest) (*PRResult, error) {
	// --- 1. Find the deployment manifest file ---
	filePath, fileContent, fileSHA, err := c.findDeploymentFile(ctx, req.Owner, req.Repo, req.DeploymentName)
	if err != nil {
		return nil, fmt.Errorf("finding deployment manifest: %w", err)
	}

	// --- 2. Apply the YAML patch ---
	patched, err := ApplyYAMLPatch(fileContent, req.Diagnosis.YAMLPatch)
	if err != nil {
		return nil, fmt.Errorf("applying YAML patch to %s: %w", filePath, err)
	}

	if string(patched) == string(fileContent) {
		return nil, fmt.Errorf("patch produced no change to %s — skipping PR", filePath)
	}

	// --- 3. Resolve default branch for the base ref ---
	defaultBranch, err := c.defaultBranch(ctx, req.Owner, req.Repo)
	if err != nil {
		return nil, fmt.Errorf("getting default branch: %w", err)
	}

	// --- 4. Create the healing branch ---
	branchName := fmt.Sprintf("gopherguard/fix-%s-%d", req.DeploymentName, time.Now().Unix())
	if err := c.createBranch(ctx, req.Owner, req.Repo, branchName, defaultBranch); err != nil {
		return nil, fmt.Errorf("creating branch %s: %w", branchName, err)
	}

	// --- 5. Commit the patched file ---
	commitMsg := fmt.Sprintf("fix(%s): AI-suggested remediation by GopherGuard\n\n%s",
		req.DeploymentName, req.Diagnosis.RootCause)
	if err := c.commitFile(ctx, req.Owner, req.Repo, branchName, filePath, fileSHA, patched, commitMsg); err != nil {
		return nil, fmt.Errorf("committing patched file: %w", err)
	}

	// --- 6. Open the pull request ---
	prURL, err := c.openPR(ctx, req, branchName, defaultBranch, filePath)
	if err != nil {
		return nil, fmt.Errorf("opening pull request: %w", err)
	}

	return &PRResult{
		PRURL:      prURL,
		BranchName: branchName,
		FilePath:   filePath,
	}, nil
}

// findDeploymentFile tries conventional paths until it finds the manifest.
// Returns (path, content bytes, blob SHA, error).
func (c *GitHubPRClient) findDeploymentFile(
	ctx context.Context, owner, repo, deploymentName string,
) (string, []byte, string, error) {
	for _, pattern := range conventionalPaths {
		path := fmt.Sprintf(pattern, deploymentName)
		fc, _, _, err := c.gh.Repositories.GetContents(ctx, owner, repo, path, nil)
		if err != nil {
			continue // not found at this path — try next
		}
		if fc == nil || fc.GetEncoding() != "base64" {
			continue
		}
		rawContent, contentErr := fc.GetContent()
		if contentErr != nil {
			return "", nil, "", fmt.Errorf("getting content of %s: %w", path, contentErr)
		}
		decoded, err := base64.StdEncoding.DecodeString(
			strings.ReplaceAll(rawContent, "\n", ""),
		)
		if err != nil {
			return "", nil, "", fmt.Errorf("decoding %s: %w", path, err)
		}
		return path, decoded, fc.GetSHA(), nil
	}
	return "", nil, "", fmt.Errorf(
		"deployment manifest for %q not found at any conventional path in %s/%s",
		deploymentName, owner, repo,
	)
}

// defaultBranch returns the repository's default branch name (usually "main").
func (c *GitHubPRClient) defaultBranch(ctx context.Context, owner, repo string) (string, error) {
	r, _, err := c.gh.Repositories.Get(ctx, owner, repo)
	if err != nil {
		return "", err
	}
	return r.GetDefaultBranch(), nil
}

// createBranch creates a new git ref branching off baseBranch.
func (c *GitHubPRClient) createBranch(ctx context.Context, owner, repo, newBranch, baseBranch string) error {
	baseRef, _, err := c.gh.Git.GetRef(ctx, owner, repo, "refs/heads/"+baseBranch)
	if err != nil {
		return fmt.Errorf("getting base ref %s: %w", baseBranch, err)
	}

	_, _, err = c.gh.Git.CreateRef(ctx, owner, repo, &gogithub.Reference{
		Ref:    gogithub.String("refs/heads/" + newBranch),
		Object: &gogithub.GitObject{SHA: baseRef.Object.SHA},
	})
	return err
}

// commitFile updates a file on an existing branch.
func (c *GitHubPRClient) commitFile(
	ctx context.Context,
	owner, repo, branch, path, currentSHA string,
	content []byte,
	message string,
) error {
	opts := &gogithub.RepositoryContentFileOptions{
		Message: gogithub.String(message),
		Content: content,
		Branch:  gogithub.String(branch),
		SHA:     gogithub.String(currentSHA),
		Committer: &gogithub.CommitAuthor{
			Name:  gogithub.String("GopherGuard"),
			Email: gogithub.String("gopherguard@noreply.github.com"),
		},
	}
	_, _, err := c.gh.Repositories.UpdateFile(ctx, owner, repo, path, opts)
	return err
}

// openPR creates the pull request and returns its HTML URL.
func (c *GitHubPRClient) openPR(
	ctx context.Context,
	req PRRequest,
	headBranch, baseBranch, filePath string,
) (string, error) {
	title := fmt.Sprintf("fix(%s): AI-suggested remediation [GopherGuard #%d]",
		req.DeploymentName, req.HealingScore+1)

	body := buildPRBody(req, filePath)

	pr, _, err := c.gh.PullRequests.Create(ctx, req.Owner, req.Repo, &gogithub.NewPullRequest{
		Title:               gogithub.String(title),
		Head:                gogithub.String(headBranch),
		Base:                gogithub.String(baseBranch),
		Body:                gogithub.String(body),
		MaintainerCanModify: gogithub.Bool(true),
	})
	if err != nil {
		return "", err
	}
	return pr.GetHTMLURL(), nil
}

// buildPRBody produces the structured pull request description.
func buildPRBody(req PRRequest, filePath string) string {
	var sb strings.Builder
	sb.WriteString("## 🐹 GopherGuard — Automated Healing PR\n\n")

	sb.WriteString("### What happened?\n")
	sb.WriteString(req.Diagnosis.RootCause + "\n\n")

	sb.WriteString("### What did GopherGuard change?\n")
	sb.WriteString(fmt.Sprintf("- **File patched**: `%s`\n", filePath))
	sb.WriteString(fmt.Sprintf("- **Deployment**: `%s/%s`\n\n", req.Namespace, req.DeploymentName))

	if req.Diagnosis.YAMLPatch != "" {
		sb.WriteString("### YAML patch applied\n")
		sb.WriteString("```yaml\n")
		sb.WriteString(req.Diagnosis.YAMLPatch)
		sb.WriteString("\n```\n\n")
	}

	sb.WriteString("### GopherGuard says\n")
	sb.WriteString(fmt.Sprintf("> 💬 *%s*\n\n", req.Diagnosis.WittyLine))

	sb.WriteString("---\n")
	sb.WriteString(fmt.Sprintf("🩺 Healing score after this fix: **%d**\n", req.HealingScore+1))
	sb.WriteString("⚠️ *Review this patch carefully before merging. GopherGuard is confident but not infallible.*\n")

	return sb.String()
}

// SplitRepo splits "owner/repo" into (owner, repo).
// Returns an error if the format is invalid.
func SplitRepo(gitRepo string) (owner, repo string, err error) {
	parts := strings.SplitN(gitRepo, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid gitRepo %q: expected \"owner/repo\"", gitRepo)
	}
	return parts[0], parts[1], nil
}
