package github

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/bradleyfalzon/ghinstallation/v2"
	"github.com/google/go-github/v45/github"
	log "github.com/sirupsen/logrus"
)

type Client interface {
	Clone(ctx context.Context, baseBranch, branch, folder string) (*github.Reference, error)
	GetRef(ctx context.Context, baseBranch, branch string) (*github.Reference, error)
	CreateTree(ctx context.Context, ref *github.Reference, baseFolder string, files []string) (tree *github.Tree, err error)
	PushCommit(ctx context.Context, ref *github.Reference, tree *github.Tree, userFullname string, userEmail string, commitMessage string) (err error)
	CreatePR(ctx context.Context, title, description, baseBranch, branch string) (*PullRequest, string, error)
	MergePR(ctx context.Context, id int) error
	ClosePR(ctx context.Context, id int) error
	GetCommitSha(ctx context.Context, organization, repository, commit string) (string, string, error)
	CommitInBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error)
}

func NewClient(ctx context.Context, config Config) (Client, error) {
	client, err := createApiClient(config.Auth)
	if err != nil {
		return nil, err
	}

	repository, _, err := client.Repositories.Get(ctx, config.Organization, config.Repository)
	if err != nil {
		return nil, err
	}

	return &apiClient{
		client:       client,
		organization: config.Organization,
		repository:   config.Repository,
		authorName:   config.AuthorName,
		authorEmail:  config.AuthorEmail,
		baseBranch:   *repository.DefaultBranch,
		cloneUrl:     *repository.CloneURL,
	}, nil
}

func createApiClient(authConfig AuthConfig) (*github.Client, error) {
	tr := http.DefaultTransport
	itr, err := ghinstallation.NewKeyFromFile(tr, int64(authConfig.AppId), int64(authConfig.InstallationId), authConfig.KeyPath)
	if err != nil {
		return nil, err
	}

	return github.NewClient(&http.Client{Transport: itr}), nil
}

type apiClient struct {
	client       *github.Client
	organization string
	repository   string
	authorName   string
	authorEmail  string
	baseBranch   string
	cloneUrl     string
}

func (c *apiClient) Clone(ctx context.Context, baseBranch, branch, folder string) (*github.Reference, error) {
	ref, err := c.GetRef(ctx, baseBranch, branch)
	if err != nil {
		return nil, err
	}

	archiveLink, _, err := c.client.Repositories.GetArchiveLink(ctx, c.organization, c.repository, github.Tarball, &github.RepositoryContentGetOptions{Ref: ref.GetRef()}, true)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodGet, archiveLink.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build fetch request, error: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch source from github, error: %w", err)
	}

	body := resp.Body
	defer func() {
		err := body.Close()
		if err != nil {
			log.WithError(err).Error("error closing response body")
		}
	}()

	err = c.extractTarGz(folder, body)
	if err != nil {
		return nil, err
	}

	return ref, nil
}

func (c *apiClient) GetRef(ctx context.Context, baseBranch, branch string) (*github.Reference, error) {
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

func (c *apiClient) CreateTree(ctx context.Context, ref *github.Reference, baseFolder string, files []string) (tree *github.Tree, err error) {
	// Create a tree with what to commit.
	var entries []*github.TreeEntry

	// Load each file into the tree.
	for _, file := range files {
		fullPath := filepath.Join(baseFolder, file)
		_, err := os.Stat(fullPath)
		if err != nil {
			if os.IsNotExist(err) {
				// If the file doesn't exist, create a deletion tree entry
				entries = append(entries, &github.TreeEntry{
					Path: github.String(file),
					Mode: github.String("100644"),
					Type: github.String("blob"),
					SHA:  nil, // nil SHA indicates deletion
				})
				continue
			}
			return nil, err
		}

		content, err := ioutil.ReadFile(fullPath)
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

func (c *apiClient) PushCommit(ctx context.Context, ref *github.Reference, tree *github.Tree, userFullname string, userEmail string, commitMessage string) (err error) {
	// Get the parent commit to attach the commit to.
	parent, _, err := c.client.Repositories.GetCommit(ctx, c.organization, c.repository, *ref.Object.SHA, nil)
	if err != nil {
		return err
	}
	// This is not always populated, but is needed.
	parent.Commit.SHA = parent.SHA

	// Create the commit using the tree.
	now := time.Now()
	commit := &github.Commit{
		Author:    &github.CommitAuthor{Date: &now, Name: &userFullname, Email: &userEmail},
		Committer: &github.CommitAuthor{Date: &now, Name: &c.authorName, Email: &c.authorEmail},
		Message:   &commitMessage,
		Tree:      tree,
		Parents:   []*github.Commit{parent.Commit},
	}
	newCommit, _, err := c.client.Git.CreateCommit(ctx, c.organization, c.repository, commit)
	if err != nil {
		return err
	}

	// Attach the commit to the master branch.
	ref.Object.SHA = newCommit.SHA
	_, _, err = c.client.Git.UpdateRef(ctx, c.organization, c.repository, ref, false)
	return err
}

func (c *apiClient) CreatePR(ctx context.Context, title, description, baseBranch, branch string) (*PullRequest, string, error) {
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

	return &PullRequest{Id: pr.GetNumber(), Link: pr.GetHTMLURL()}, diff, nil
}

func (c *apiClient) MergePR(ctx context.Context, id int) error {
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

func (c *apiClient) ClosePR(ctx context.Context, id int) error {
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

func (c *apiClient) GetCommitSha(ctx context.Context, organization, repository, commit string) (string, string, error) {
	ghCommit, _, err := c.client.Repositories.GetCommit(ctx, organization, repository, commit, &github.ListOptions{})
	if err != nil {
		if err, ok := err.(*github.ErrorResponse); ok {
			if err.Response.StatusCode == http.StatusUnprocessableEntity {
				return "", "", api.NewValidationErr("commit does not exist")
			}
		}

		return "", "", err
	}

	return ghCommit.GetSHA(), ghCommit.GetHTMLURL(), nil
}

func (c *apiClient) CommitInBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	for _, branch := range branches {
		commits, _, err := c.client.Repositories.CompareCommits(ctx, organization, repository, commit, branch, &github.ListOptions{})
		if err != nil {
			if err, ok := err.(*github.ErrorResponse); ok {
				if err.Response.StatusCode == http.StatusNotFound {
					continue
				}
			}

			return false, err
		}

		commitInBranch := *commits.Status == "ahead" || *commits.Status == "identical"
		if commitInBranch {
			return true, nil
		}
	}

	return false, nil
}

func (c *apiClient) deleteBranch(ctx context.Context, branchName string) error {
	_, err := c.client.Git.DeleteRef(ctx, c.organization, c.repository, "heads/"+branchName)
	if err != nil && strings.Contains(err.Error(), "Reference does not exist") {
		return nil
	}

	return err
}

func (c *apiClient) extractTarGz(targetPath string, gzipStream io.Reader) error {
	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		return fmt.Errorf("failed to create gzip reader, error: %w", err)
	}

	tarReader := tar.NewReader(uncompressedStream)
	for true {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}

		if err != nil {
			return fmt.Errorf("failed to get next value from tar reader, error: %w", err)
		}

		name := c.removeFirstPathPart(header.Name)
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.Mkdir(path.Join(targetPath, name), 0755); err != nil && !errors.Is(err, os.ErrExist) {
				return fmt.Errorf("failed to create directory %s, error: %w", name, err)
			}
		case tar.TypeReg:
			err := c.createFile(targetPath, header, tarReader)
			if err != nil {
				return err
			}
		case tar.TypeXGlobalHeader:
			continue
		default:
			return fmt.Errorf("unkown header type: %b, name: %s", header.Typeflag, name)
		}
	}

	return nil
}

func (c *apiClient) createFile(targetPath string, header *tar.Header, tarReader *tar.Reader) error {
	fullPath := path.Join(targetPath, c.removeFirstPathPart(header.Name))
	err := os.MkdirAll(path.Dir(fullPath), 0755)
	if err != nil {
		return fmt.Errorf("failed to create parent directories %s, error: %w", targetPath, err)
	}

	outFile, err := os.Create(fullPath)
	defer func() {
		closeErr := outFile.Close()
		if closeErr != nil {
			log.WithError(closeErr).WithField("file_name", header.Name).Error("failed to close file")
		}
	}()

	if err != nil {
		return fmt.Errorf("failed to create file %s, error: %w", header.Name, err)
	}

	if _, err := io.Copy(outFile, tarReader); err != nil {
		return fmt.Errorf("failed to copy file content, file: %s, error: %w", header.Name, err)
	}

	return nil
}

func (c *apiClient) removeFirstPathPart(name string) string {
	firstPathSeparatorIdx := strings.Index(name, "/")
	if firstPathSeparatorIdx == -1 {
		return name
	}
	return name[firstPathSeparatorIdx:]
}
