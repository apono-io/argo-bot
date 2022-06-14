package deploy

import (
	"context"
	"errors"
	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/transport/http"
	"github.com/google/go-github/v45/github"
	"golang.org/x/oauth2"
	"io/ioutil"
	"path/filepath"
	"strings"
	"time"
)

type GitClient interface {
	Clone(ctx context.Context, baseBranch, branch, folder string) (*github.Reference, error)
	GetRef(ctx context.Context, baseBranch, branch string) (*github.Reference, error)
	CreateTree(ctx context.Context, ref *github.Reference, baseFolder string, files []string) (tree *github.Tree, err error)
	PushCommit(ctx context.Context, ref *github.Reference, tree *github.Tree, commitMessage string) (err error)
	CreatePR(ctx context.Context, title, description, baseBranch, branch string) (*github.PullRequest, string, error)
	MergePR(ctx context.Context, id int) error
	ClosePR(ctx context.Context, id int) error
	GetCommitSha(ctx context.Context, organization, repository, commit string) (string, string, error)
	CommitInBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error)
}

func NewGithubClient(ctx context.Context, config GithubConfig) (GitClient, error) {
	ts := oauth2.StaticTokenSource(&oauth2.Token{AccessToken: config.Token})
	tc := oauth2.NewClient(ctx, ts)

	client := github.NewClient(tc)
	repository, _, err := client.Repositories.Get(ctx, config.Organization, config.Repository)
	if err != nil {
		return nil, err
	}

	return &githubClient{
		client:       client,
		organization: config.Organization,
		repository:   config.Repository,
		authorName:   config.AuthorName,
		authorEmail:  config.AuthorEmail,
		accessToken:  config.Token,
		baseBranch:   *repository.DefaultBranch,
		cloneUrl:     *repository.CloneURL,
	}, nil
}

type githubClient struct {
	client       *github.Client
	organization string
	repository   string
	authorName   string
	authorEmail  string
	accessToken  string
	baseBranch   string
	cloneUrl     string
}

func (c *githubClient) Clone(ctx context.Context, baseBranch, branch, folder string) (*github.Reference, error) {
	ref, err := c.GetRef(ctx, baseBranch, branch)
	if err != nil {
		return nil, err
	}

	_, err = git.PlainClone(folder, false, &git.CloneOptions{
		URL: c.cloneUrl,
		Auth: &http.BasicAuth{
			Username: "x-access-token",
			Password: c.accessToken,
		},
		ReferenceName: plumbing.NewBranchReferenceName(branch),
	})

	if err != nil {
		return nil, err
	}

	return ref, nil
}

func (c *githubClient) GetRef(ctx context.Context, baseBranch, branch string) (*github.Reference, error) {
	if baseBranch == "" {
		baseBranch = c.baseBranch
	}

	if baseBranch == branch {
		return nil, errors.New("branch name cannot be the same as the base branch")
	}

	_, _, err := c.client.Git.GetRef(ctx, c.organization, c.repository, "refs/heads/"+branch)
	if err == nil {
		err = c.deleteBranch(ctx, branch)
		if err != nil {
			return nil, err
		}
	}

	baseRef, _, err := c.client.Git.GetRef(ctx, c.organization, c.repository, "refs/heads/"+baseBranch)
	if err != nil {
		return nil, err
	}

	newRef := &github.Reference{Ref: github.String("refs/heads/" + branch), Object: &github.GitObject{SHA: baseRef.Object.SHA}}
	ref, _, err := c.client.Git.CreateRef(ctx, c.organization, c.repository, newRef)

	return ref, err
}

func (c *githubClient) CreateTree(ctx context.Context, ref *github.Reference, baseFolder string, files []string) (tree *github.Tree, err error) {
	// Create a tree with what to commit.
	var entries []*github.TreeEntry

	// Load each file into the tree.
	for _, file := range files {
		content, err := ioutil.ReadFile(filepath.Join(baseFolder, file))
		if err != nil {
			return nil, err
		}

		entries = append(entries, &github.TreeEntry{
			Path:    github.String(file),
			Type:    github.String("blob"),
			Content: github.String(string(content)),
			Mode:    github.String("100644"),
		})
	}

	tree, _, err = c.client.Git.CreateTree(ctx, c.organization, c.repository, *ref.Object.SHA, entries)
	return tree, err
}

func (c *githubClient) PushCommit(ctx context.Context, ref *github.Reference, tree *github.Tree, commitMessage string) (err error) {
	// Get the parent commit to attach the commit to.
	parent, _, err := c.client.Repositories.GetCommit(ctx, c.organization, c.repository, *ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	// This is not always populated, but is needed.
	parent.Commit.SHA = parent.SHA

	// Create the commit using the tree.
	date := time.Now()
	author := &github.CommitAuthor{Date: &date, Name: &c.authorName, Email: &c.authorEmail}
	commit := &github.Commit{Author: author, Message: &commitMessage, Tree: tree, Parents: []*github.Commit{parent.Commit}}
	newCommit, _, err := c.client.Git.CreateCommit(ctx, c.organization, c.repository, commit)
	if err != nil {
		return err
	}

	// Attach the commit to the master branch.
	ref.Object.SHA = newCommit.SHA
	_, _, err = c.client.Git.UpdateRef(ctx, c.organization, c.repository, ref, false)
	return err
}

func (c *githubClient) CreatePR(ctx context.Context, title, description, baseBranch, branch string) (*github.PullRequest, string, error) {
	if baseBranch == "" {
		baseBranch = c.baseBranch
	}

	newPR := &github.NewPullRequest{
		Title: &title,
		Head:  &branch,
		Base:  &baseBranch,
		Body:  &description,
	}

	pr, _, err := c.client.PullRequests.Create(ctx, c.organization, c.repository, newPR)
	if err != nil {
		return nil, "", err
	}

	diff, _, err := c.client.PullRequests.GetRaw(ctx, c.organization, c.repository, pr.GetNumber(), github.RawOptions{Type: github.Diff})
	if err != nil {
		return nil, "", err
	}

	return pr, diff, nil
}

func (c *githubClient) MergePR(ctx context.Context, id int) error {
	pr, _, err := c.client.PullRequests.Get(ctx, c.organization, c.repository, id)
	if err != nil {
		return err
	}

	if pr.GetMerged() {
		return errors.New("pull request is already merged")
	}

	_, _, err = c.client.PullRequests.Merge(ctx, c.organization, c.repository, id, "", &github.PullRequestOptions{MergeMethod: "squash"})
	if err != nil {
		return err
	}

	return c.deleteBranch(ctx, *pr.Head.Ref)
}

func (c *githubClient) ClosePR(ctx context.Context, id int) error {
	pr, _, err := c.client.PullRequests.Get(ctx, c.organization, c.repository, id)
	if err != nil {
		return err
	}

	if pr.GetMerged() {
		return errors.New("pull request is already merged")
	}

	pr.State = github.String("closed")
	_, _, err = c.client.PullRequests.Edit(ctx, c.organization, c.repository, id, pr)
	if err != nil {
		return err
	}

	return c.deleteBranch(ctx, *pr.Head.Ref)
}

func (c *githubClient) GetCommitSha(ctx context.Context, organization, repository, commit string) (string, string, error) {
	ghCommit, _, err := c.client.Repositories.GetCommit(ctx, organization, repository, commit, &github.ListOptions{})
	if err != nil {
		return "", "", err
	}

	return ghCommit.GetSHA(), ghCommit.GetHTMLURL(), nil
}

func (c *githubClient) CommitInBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	for _, branch := range branches {
		commits, _, err := c.client.Repositories.CompareCommits(ctx, organization, repository, commit, branch, &github.ListOptions{})
		if err != nil {
			return false, err
		}

		commitInBranch := *commits.Status == "ahead" || *commits.Status == "identical"
		if commitInBranch {
			return true, nil
		}
	}

	return false, nil
}

func (c *githubClient) deleteBranch(ctx context.Context, branchName string) error {
	_, err := c.client.Git.DeleteRef(ctx, c.organization, c.repository, "heads/"+branchName)
	if err != nil && strings.Contains(err.Error(), "Reference does not exist") {
		return nil
	}

	return err
}
