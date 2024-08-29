package deploy

import (
	"context"
	"fmt"
	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/apono-io/argo-bot/pkg/github"
	log "github.com/sirupsen/logrus"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

type Deployer interface {
	GetCommitSha(ctx context.Context, serviceName []string, commit string) (string, string, error)
	Deploy(serviceNames []string, environment, commit, commitUrl, userFullname, userEmail string) (*github.PullRequest, string, error)
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

func (d *githubDeployer) GetCommitSha(ctx context.Context, servicesNames []string, commit string) (string, string, error) {
	services, err := d.LookupServices(servicesNames)
	if err != nil {
		return "", "", err
	}

	if !isServicesFromTheSameRepo(services) {
		return "", "", api.NewValidationErr("services are not from the same repository")
	}

	return d.githubClient.GetCommitSha(ctx, services[0].GithubOrganization, services[0].GithubRepository, commit)
}

func (d *githubDeployer) Approve(ctx context.Context, pullRequestId int) error {
	return d.githubClient.MergePR(ctx, pullRequestId)
}

func (d *githubDeployer) Cancel(ctx context.Context, pullRequestId int) error {
	return d.githubClient.ClosePR(ctx, pullRequestId)
}

func (d *githubDeployer) Deploy(serviceNames []string, environmentName, commit, commitUrl, userFullname, userEmail string) (*github.PullRequest, string, error) {
	ctx := context.Background()
	logWithCtx := log.WithFields(log.Fields{
		"environment":  environmentName,
		"serviceNames": serviceNames,
		"commit":       commit,
	})

	services, err := d.LookupServices(serviceNames)
	if err != nil {
		return nil, "", err
	}

	serviceToEnvironment := map[*Service]*ServiceEnvironment{}
	var environments []*ServiceEnvironment
	for _, service := range services {
		environment, err := d.LookupEnvironment(service, environmentName)
		if err != nil {
			return nil, "", err
		}
		serviceToEnvironment[service] = environment
		environments = append(environments, environment)
	}

	if !isEnvironmentsFromTheSameBranch(environments) {
		return nil, "", api.NewValidationErr("environments have different deployment branches")
	}
	deploymentBranch := environments[0].DeploymentRepoBranch

	for service, environment := range serviceToEnvironment {
		if len(environment.AllowedBranches) > 0 {
			logWithCtx.Infof("Validating branch")
			validBranch, err := d.validateBranch(ctx, service.GithubOrganization, service.GithubRepository, commit, environment.AllowedBranches)
			if err != nil {
				return nil, "", err
			}

			if !validBranch {
				return nil, "", api.NewValidationErr(fmt.Sprintf("commit is not in allowed branches for serivce %s", service.Name))
			}
		}
	}

	servicesString := strings.Join(serviceNames, ",")

	logWithCtx.Infof("Starting deployment")
	branch := fmt.Sprintf("deploy-%s-%s", servicesString, environmentName)
	commitMsg := fmt.Sprintf("Deploy %s to %s with version %s triggered by %s (%s)", servicesString, environmentName, commit[:7], userFullname, userEmail)

	baseFolder, err := os.MkdirTemp(d.config.Github.CloneTmpDir, branch+"-*")
	if err != nil {
		return nil, "", fmt.Errorf("failed to create temp directory, error: %w", err)
	}

	ref, err := d.githubClient.Clone(ctx, deploymentBranch, branch, baseFolder)
	if err != nil {
		return nil, "", fmt.Errorf("failed to clone deployment repository, error: %w", err)
	}
	defer func() {
		err := os.RemoveAll(baseFolder)
		if err != nil {
			logWithCtx.WithError(err).Error("failed to remove source folder")
		}
	}()

	for service, environment := range serviceToEnvironment {
		files, err := d.renderTemplates(baseFolder, environment.TemplatePath, environment.GeneratedPath, service.Name, environmentName, commit)
		if err != nil {
			return nil, "", fmt.Errorf("failed to render templates for serivce %s, error: %w", service.Name, err)
		}

		tree, err := d.githubClient.CreateTree(ctx, ref, baseFolder, files)
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diff tree for serivce %s, error: %w", service.Name, err)
		}

		if err = d.githubClient.PushCommit(ctx, ref, tree, userFullname, userEmail, commitMsg); err != nil {
			return nil, "", fmt.Errorf("failed to create commit for serivce %s, error: %w", service.Name, err)
		}
	}

	prDescription := fmt.Sprintf("Service Names: %s\nEnvironment: %s\nCommit: [%s](%s)\nRequested by: %s (%s)",
		servicesString, environmentName, commit[:7], commitUrl, userFullname, userEmail)
	pr, diff, err := d.githubClient.CreatePR(ctx, commitMsg, prDescription, deploymentBranch, branch)
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
	templateFiles, err := os.ReadDir(templateFolder)
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

func (d *githubDeployer) LookupServices(names []string) ([]*Service, error) {
	var services []*Service
	for _, name := range names {
		service, err := d.LookupService(name)
		if err != nil {
			return nil, err
		}
		services = append(services, service)
	}

	if len(services) == 0 {
		return nil, api.NewValidationErr("no services found")
	}

	return services, nil
}

func (d *githubDeployer) LookupService(name string) (*Service, error) {
	for _, service := range d.config.Services {
		if strings.ToLower(service.Name) == strings.ToLower(name) {
			return &service, nil
		}
	}
	return nil, api.NewValidationErr(fmt.Sprintf("service %s does not exist", name))
}

func (d *githubDeployer) LookupEnvironment(service *Service, name string) (*ServiceEnvironment, error) {
	for _, environment := range service.Environments {
		if strings.ToLower(environment.Name) == strings.ToLower(name) {
			return &environment, nil
		}
	}
	return nil, api.NewValidationErr(fmt.Sprintf("environment %s does not exist for service %s", name, service.Name))
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

func isServicesFromTheSameRepo(services []*Service) bool {
	if len(services) == 0 {
		return true
	}

	org := services[0].GithubOrganization
	repo := services[0].GithubRepository

	for _, service := range services {
		if service.GithubOrganization != org || service.GithubRepository != repo {
			return false
		}
	}

	return true
}

func isEnvironmentsFromTheSameBranch(environments []*ServiceEnvironment) bool {
	if len(environments) == 0 {
		return true
	}

	branch := environments[0].DeploymentRepoBranch

	for _, environment := range environments {
		if environment.DeploymentRepoBranch != branch {
			return false
		}
	}

	return true
}

type options struct {
	ServiceName string
	Environment string
	Version     string
}
