package deploy

import (
	"context"
	"errors"
	"fmt"
	"github.com/google/go-github/v45/github"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type ValidationErr struct {
	Err error
}

func (e ValidationErr) Error() string {
	return e.Err.Error()
}

func NewValidationErr(err string) error {
	return ValidationErr{Err: errors.New(err)}
}

type Deployer interface {
	GetCommitSha(ctx context.Context, serviceName, commit string) (string, string, error)
	Deploy(serviceName, environment, commit, commitUrl, user string) (*github.PullRequest, string, error)
	Approve(ctx context.Context, pullRequestId int) error
	Cancel(ctx context.Context, pullRequestId int) error
}

func New(config Config) (Deployer, error) {
	client, err := NewGithubClient(context.Background(), config.Github)
	if err != nil {
		return nil, err
	}

	return &gitDeployer{
		config:    config,
		gitClient: client,
	}, nil
}

type gitDeployer struct {
	config    Config
	gitClient GitClient
}

func (d *gitDeployer) GetCommitSha(ctx context.Context, serviceName, commit string) (string, string, error) {
	service, err := d.LookupService(serviceName)
	if err != nil {
		return "", "", err
	}

	return d.gitClient.GetCommitSha(ctx, service.GithubOrganization, service.GithubRepository, commit)
}

func (d *gitDeployer) Approve(ctx context.Context, pullRequestId int) error {
	return d.gitClient.MergePR(ctx, pullRequestId)
}

func (d *gitDeployer) Cancel(ctx context.Context, pullRequestId int) error {
	return d.gitClient.ClosePR(ctx, pullRequestId)
}

func (d *gitDeployer) Deploy(serviceName, environmentName, commit, commitUrl, user string) (*github.PullRequest, string, error) {
	ctx := context.Background()
	logWithCtx := log.WithFields(log.Fields{
		"environment": environmentName,
		"serviceName": serviceName,
		"commit":      commit,
	})

	service, err := d.LookupService(serviceName)
	if err != nil {
		return nil, "", err
	}

	environment, err := d.LookupEnvironment(service, environmentName)
	if err != nil {
		return nil, "", err
	}

	if strings.TrimSpace(environment.AllowedBranchesCsv) != "" {
		logWithCtx.Infof("Validating branch")
		allowedBranches := strings.Split(strings.TrimSpace(environment.AllowedBranchesCsv), ",")
		validBranch, err := d.validateBranch(ctx, service.GithubOrganization, service.GithubRepository, commit, allowedBranches)
		if err != nil {
			return nil, "", err
		}

		if !validBranch {
			return nil, "", NewValidationErr("commit is not in allowed branches")
		}
	}

	logWithCtx.Infof("Starting deployment")
	branch := fmt.Sprintf("argo-deploy-%s-%s", serviceName, environmentName)
	commitMsg := fmt.Sprintf("Argo Bot: Deploy %s to %s commit %s triggered by %s", serviceName, environmentName, commit[:7], user)

	baseFolder, err := ioutil.TempDir(d.config.Github.CloneTmpDir, branch+"-*")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp directory, error: %w", err)
	}

	ref, err := d.gitClient.Clone(ctx, environment.DeploymentRepoBranch, branch, baseFolder)
	if err != nil {
		return nil, "", fmt.Errorf("failed to clone deployment repository, error: %w", err)
	}

	files, err := d.renderTemplates(baseFolder, environment.TemplatePath, environment.GeneratedPath, serviceName, environmentName, commit)
	if err != nil {
		return nil, "", fmt.Errorf("failed to render templates, error: %w", err)
	}

	tree, err := d.gitClient.CreateTree(ctx, ref, baseFolder, files)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create diff tree, error: %w", err)
	}

	if err = d.gitClient.PushCommit(ctx, ref, tree, commitMsg); err != nil {
		return nil, "", fmt.Errorf("failed to create commit, error: %w", err)
	}

	prDescription := fmt.Sprintf("Service Name: %s\nEnvironment: %s\nCommit: [%s](%s)\nRequested by: %s",
		serviceName, environmentName, commit[:7], commitUrl, user)
	pr, diff, err := d.gitClient.CreatePR(ctx, commitMsg, prDescription, environment.DeploymentRepoBranch, branch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create pull request, error: %w", err)
	}

	logWithCtx.Infof("Created pull request for deployment")

	return pr, diff, nil
}

func (d *gitDeployer) validateBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	return d.gitClient.CommitInBranch(ctx, organization, repository, commit, branches)
}

func (d *gitDeployer) renderTemplates(baseFolder, templatePath, generatedPath, serviceName, environment, commit string) ([]string, error) {
	generatedFolder := filepath.Join(baseFolder, generatedPath)
	err := d.cleanFolder(generatedFolder)
	if err != nil {
		return nil, err
	}

	templateFolder := filepath.Join(baseFolder, templatePath)
	templateFiles, err := ioutil.ReadDir(templateFolder)
	if err != nil {
		return nil, err
	}

	tmpl := template.New("gotpl")
	tmpl.Option("missingkey=error")

	var renderedFiles []string
	opts := options{
		ServiceName: serviceName,
		Environment: environment,
		Version:     commit,
	}
	for _, file := range templateFiles {
		_, err = tmpl.New(file.Name()).ParseFiles(filepath.Join(templateFolder, file.Name()))
		if err != nil {
			return nil, err
		}
	}

	for _, file := range templateFiles {
		absolutePath := filepath.Join(generatedFolder, file.Name())
		relPath, err := filepath.Rel(baseFolder, absolutePath)
		if err != nil {
			return nil, err
		}

		renderedFiles = append(renderedFiles, relPath)
		err = d.renderTemplateFile(absolutePath, tmpl, file.Name(), opts)
		if err != nil {
			return nil, err
		}
	}

	return renderedFiles, nil
}

func (d *gitDeployer) cleanFolder(folder string) error {
	err := os.RemoveAll(folder)
	if err != nil {
		return err
	}

	return os.MkdirAll(folder, 0755)
}

func (d *gitDeployer) LookupService(name string) (*Service, error) {
	for _, service := range d.config.Services {
		if strings.ToLower(service.Name) == strings.ToLower(name) {
			return &service, nil
		}
	}
	return nil, NewValidationErr("service does not exist")
}

func (d *gitDeployer) LookupEnvironment(service *Service, name string) (*ServiceEnvironment, error) {
	for _, environment := range service.Environments {
		if strings.ToLower(environment.Name) == strings.ToLower(name) {
			return &environment, nil
		}
	}
	return nil, NewValidationErr("environment does not exist")
}

func (d *gitDeployer) renderTemplateFile(absolutePath string, tmpl *template.Template, templateName string, opts options) error {
	file, err := os.OpenFile(absolutePath, os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return err
	}

	defer func(file *os.File) {
		err = file.Close()
		if err != nil {
			log.WithField("absolutePath", absolutePath).
				WithField("templateName", templateName).
				WithError(err).
				Error("Failed to close generated file")
		}
	}(file)

	return tmpl.ExecuteTemplate(file, templateName, opts)
}

type options struct {
	ServiceName string
	Environment string
	Version     string
}
