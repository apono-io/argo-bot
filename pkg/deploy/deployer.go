package deploy

import (
	"context"
	"fmt"
	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/apono-io/argo-bot/pkg/github"
	log "github.com/sirupsen/logrus"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type Deployer interface {
	GetCommitSha(ctx context.Context, serviceName, commit string) (string, string, error)
	Deploy(serviceName, environment, commit, commitUrl, user string) (*github.PullRequest, string, error)
	Approve(ctx context.Context, pullRequestId int) error
	Cancel(ctx context.Context, pullRequestId int) error
}

func New(config Config) (Deployer, error) {
	client, err := github.NewClient(context.Background(), config.Github)
	if err != nil {
		return nil, err
	}

	return &githubDeployer{
		config:       config,
		githubClient: client,
	}, nil
}

type githubDeployer struct {
	config       Config
	githubClient github.Client
}

func (d *githubDeployer) GetCommitSha(ctx context.Context, serviceName, commit string) (string, string, error) {
	service, err := d.LookupService(serviceName)
	if err != nil {
		return "", "", err
	}

	return d.githubClient.GetCommitSha(ctx, service.GithubOrganization, service.GithubRepository, commit)
}

func (d *githubDeployer) Approve(ctx context.Context, pullRequestId int) error {
	return d.githubClient.MergePR(ctx, pullRequestId)
}

func (d *githubDeployer) Cancel(ctx context.Context, pullRequestId int) error {
	return d.githubClient.ClosePR(ctx, pullRequestId)
}

func (d *githubDeployer) Deploy(serviceName, environmentName, commit, commitUrl, user string) (*github.PullRequest, string, error) {
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

	if len(environment.AllowedBranches) > 0 {
		logWithCtx.Infof("Validating branch")
		validBranch, err := d.validateBranch(ctx, service.GithubOrganization, service.GithubRepository, commit, environment.AllowedBranches)
		if err != nil {
			return nil, "", err
		}

		if !validBranch {
			return nil, "", api.NewValidationErr("commit is not in allowed branches")
		}
	}

	logWithCtx.Infof("Starting deployment")
	branch := fmt.Sprintf("argo-deploy-%s-%s", serviceName, environmentName)
	commitMsg := fmt.Sprintf("Argo Bot: Deploy %s to %s commit %s triggered by %s", serviceName, environmentName, commit[:7], user)

	baseFolder, err := ioutil.TempDir(d.config.Github.CloneTmpDir, branch+"-*")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp directory, error: %w", err)
	}

	ref, err := d.githubClient.Clone(ctx, environment.DeploymentRepoBranch, branch, baseFolder)
	if err != nil {
		return nil, "", fmt.Errorf("failed to clone deployment repository, error: %w", err)
	}
	defer func() {
		err := os.RemoveAll(baseFolder)
		if err != nil {
			logWithCtx.WithError(err).Error("failed to remove source folder")
		}
	}()

	files, err := d.renderTemplates(baseFolder, environment.TemplatePath, environment.GeneratedPath, serviceName, environmentName, commit)
	if err != nil {
		return nil, "", fmt.Errorf("failed to render templates, error: %w", err)
	}

	tree, err := d.githubClient.CreateTree(ctx, ref, baseFolder, files)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create diff tree, error: %w", err)
	}

	if err = d.githubClient.PushCommit(ctx, ref, tree, commitMsg); err != nil {
		return nil, "", fmt.Errorf("failed to create commit, error: %w", err)
	}

	prDescription := fmt.Sprintf("Service Name: %s\nEnvironment: %s\nCommit: [%s](%s)\nRequested by: %s",
		serviceName, environmentName, commit[:7], commitUrl, user)
	pr, diff, err := d.githubClient.CreatePR(ctx, commitMsg, prDescription, environment.DeploymentRepoBranch, branch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create pull request, error: %w", err)
	}

	logWithCtx.Infof("Created pull request for deployment")

	return pr, diff, nil
}

func (d *githubDeployer) validateBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	return d.githubClient.CommitInBranch(ctx, organization, repository, commit, branches)
}

func (d *githubDeployer) renderTemplates(baseFolder, templatePath, generatedPath, serviceName, environment, commit string) ([]string, error) {
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

func (d *githubDeployer) cleanFolder(folder string) error {
	err := os.RemoveAll(folder)
	if err != nil {
		return err
	}

	return os.MkdirAll(folder, 0755)
}

func (d *githubDeployer) LookupService(name string) (*Service, error) {
	for _, service := range d.config.Services {
		if strings.ToLower(service.Name) == strings.ToLower(name) {
			return &service, nil
		}
	}
	return nil, api.NewValidationErr("service does not exist")
}

func (d *githubDeployer) LookupEnvironment(service *Service, name string) (*ServiceEnvironment, error) {
	for _, environment := range service.Environments {
		if strings.ToLower(environment.Name) == strings.ToLower(name) {
			return &environment, nil
		}
	}
	return nil, api.NewValidationErr("environment does not exist")
}

func (d *githubDeployer) renderTemplateFile(absolutePath string, tmpl *template.Template, templateName string, opts options) error {
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
