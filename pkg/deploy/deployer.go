package deploy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/apono-io/argo-bot/pkg/github"
	gh "github.com/google/go-github/v45/github"
	log "github.com/sirupsen/logrus"
)

type FreezeAction string

const (
	FreezeActionFreeze   FreezeAction = "freeze"
	FreezeActionUnfreeze FreezeAction = "unfreeze"
)

const freezeFileName = ".freeze"

type Deployer interface {
	GetCommitSha(ctx context.Context, serviceName []string, commit string) (string, string, error)
	Deploy(serviceNames []string, environment, commit, commitUrl, userFullname, userEmail string) (*github.PullRequest, string, error)
	Freeze(serviceNames []string, environment, userFullname, userEmail string, action FreezeAction) (*github.PullRequest, string, error)
	Approve(ctx context.Context, pullRequestId int) error
	Cancel(ctx context.Context, pullRequestId int) error
	ResolveTags(names []string) []string
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

func (d *githubDeployer) ResolveTags(names []string) []string {
	services, err := d.LookupServices(names)
	if err != nil {
		return names
	}

	if len(services) == 0 {
		return names
	}

	var resolvedNames []string
	for _, service := range services {
		resolvedNames = append(resolvedNames, service.Name)
	}

	return resolvedNames
}

func (d *githubDeployer) GetCommitSha(ctx context.Context, servicesNames []string, commit string) (string, string, error) {
	services, err := d.LookupServices(servicesNames)
	if err != nil {
		return "", "", err
	}

	if !areServicesFromSameRepo(services) {
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

	serviceToEnvironment, deploymentBranch, err := d.resolveServicesAndEnvironment(serviceNames, environmentName)
	if err != nil {
		return nil, "", err
	}

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
	prTitle := fmt.Sprintf("Deploy %s to %s with version %s triggered by %s (%s)", servicesString, environmentName, commit[:7], userFullname, userEmail)

	baseFolder, ref, err := d.cloneBranch(ctx, branch, deploymentBranch)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		err := os.RemoveAll(baseFolder)
		if err != nil {
			logWithCtx.WithError(err).Error("failed to remove source folder")
		}
	}()

	for service, environment := range serviceToEnvironment {
		commitMsg := fmt.Sprintf("Deploy %s to %s with version %s triggered by %s (%s)", service.Name, environmentName, commit[:7], userFullname, userEmail)
		freezeFilePath := getFreezeFilePath(*environment)

		freezed, err := d.checkIfServiceFreezed(baseFolder, freezeFilePath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to check if service %s is freezed, error: %w", service.Name, err)
		}
		if freezed {
			return nil, "", api.NewValidationErr(fmt.Sprintf("service %s is freezed, cannot deploy", service.Name))
		}

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
	pr, diff, err := d.githubClient.CreatePR(ctx, prTitle, prDescription, deploymentBranch, branch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create pull request, error: %w", err)
	}

	logWithCtx.Infof("Created pull request for deployment")

	return pr, diff, nil
}

func (d *githubDeployer) Freeze(serviceNames []string, environment, userFullname, userEmail string, action FreezeAction) (*github.PullRequest, string, error) {
	ctx := context.Background()
	logWithCtx := log.WithFields(log.Fields{
		"environment":  environment,
		"serviceNames": serviceNames,
	})

	freezeActionMessage := string(action)

	serviceToEnvironment, deploymentBranch, err := d.resolveServicesAndEnvironment(serviceNames, environment)
	if err != nil {
		return nil, "", err
	}

	servicesString := strings.Join(serviceNames, ",")

	logWithCtx.Infof(fmt.Sprintf("Starting %s", freezeActionMessage))
	branch := fmt.Sprintf("%s-%s-%s", freezeActionMessage, servicesString, environment)
	prTitle := fmt.Sprintf("%s %s to %s triggered by %s (%s)", freezeActionMessage, servicesString, environment, userFullname, userEmail)

	baseFolder, ref, err := d.cloneBranch(ctx, branch, deploymentBranch)
	if err != nil {
		return nil, "", err
	}
	defer func() {
		err := os.RemoveAll(baseFolder)
		if err != nil {
			logWithCtx.WithError(err).Error("failed to remove source folder")
		}
	}()

	changesDetected := false

	for service, environment := range serviceToEnvironment {
		commitMsg := fmt.Sprintf("%s %s to %s triggered by %s (%s)", freezeActionMessage, service.Name, environment, userFullname, userEmail)
		freezeFilePath := getFreezeFilePath(*environment)

		var freezeFile string
		if action == FreezeActionUnfreeze {
			frozen, err := d.checkIfServiceFreezed(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to check if service %s is frozen, error: %w", service.Name, err)
			}
			if !frozen {
				continue
			}
			freezeFile, err = d.removeFreezeFile(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to remove freeze file for serivce %s, error: %w", service.Name, err)
			}
		} else {
			freezeFile, err = d.createFreezeFile(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to create freeze file for serivce %s, error: %w", service.Name, err)
			}
		}

		tree, err := d.githubClient.CreateTree(ctx, ref, baseFolder, []string{freezeFile})
		if err != nil {
			return nil, "", fmt.Errorf("failed to create diff tree for serivce %s, error: %w", service.Name, err)
		}

		if err = d.githubClient.PushCommit(ctx, ref, tree, userFullname, userEmail, commitMsg); err != nil {
			return nil, "", fmt.Errorf("failed to create commit for serivce %s, error: %w", service.Name, err)
		}
		changesDetected = true
	}

	if !changesDetected {
		logWithCtx.Info("No changes needed - services already in desired state")
		return nil, "", nil
	}

	prDescription := fmt.Sprintf("Service Names: %s\nEnvironment: %s\nRequested by: %s (%s)",
		servicesString, environment, userFullname, userEmail)
	pr, diff, err := d.githubClient.CreatePR(ctx, prTitle, prDescription, deploymentBranch, branch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create pull request, error: %w", err)
	}

	logWithCtx.Infof("Created pull request for freeze")

	return pr, diff, nil
}

func (d *githubDeployer) cloneBranch(ctx context.Context, tmoBranch, deploymentBranch string) (string, *gh.Reference, error) {
	baseFolder, err := os.MkdirTemp(d.config.Github.CloneTmpDir, tmoBranch+"-*")
	if err != nil {
		return "", nil, fmt.Errorf("failed to create temp directory, error: %w", err)
	}

	ref, err := d.githubClient.Clone(ctx, deploymentBranch, tmoBranch, baseFolder)
	if err != nil {
		return "", nil, fmt.Errorf("failed to clone deployment repository, error: %w", err)
	}

	return baseFolder, ref, nil
}

func (d *githubDeployer) resolveServicesAndEnvironment(serviceNames []string, environmentName string) (map[*Service]*ServiceEnvironment, string, error) {
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

	if !areEnvironmentsFromSameBranch(environments) {
		return nil, "", api.NewValidationErr("environments have different deployment branches")
	}
	deploymentBranch := environments[0].DeploymentRepoBranch

	return serviceToEnvironment, deploymentBranch, nil
}

func (d *githubDeployer) validateBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	return d.githubClient.CommitInBranch(ctx, organization, repository, commit, branches)
}

func (d *githubDeployer) checkIfServiceFreezed(baseFolder, freezeFilePath string) (bool, error) {
	freezeFile := filepath.Join(baseFolder, freezeFilePath, freezeFileName)
	_, err := os.Stat(freezeFile)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}

	return true, nil
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

func (d *githubDeployer) createFreezeFile(baseFolder, freezeFilePath string) (string, error) {
	freezeFile := filepath.Join(baseFolder, freezeFilePath, freezeFileName)
	file, err := os.Create(freezeFile)
	if err != nil {
		return "", err
	}

	relPath, err := filepath.Rel(baseFolder, file.Name())
	if err != nil {
		return "", err
	}

	return relPath, nil
}

func (d *githubDeployer) removeFreezeFile(baseFolder, freezeFilePath string) (string, error) {
	freezeFile := filepath.Join(baseFolder, freezeFilePath, freezeFileName)
	relPath, err := filepath.Rel(baseFolder, freezeFile)
	if err != nil {
		return "", err
	}

	freezed, err := d.checkIfServiceFreezed(baseFolder, freezeFilePath)
	if err != nil {
		return "", err
	}
	if !freezed {
		return relPath, nil
	}

	err = os.Remove(freezeFile)
	if err != nil {
		return "", err
	}

	return relPath, nil
}

func (d *githubDeployer) cleanFolder(folder string) error {
	err := os.RemoveAll(folder)
	if err != nil {
		return err
	}

	return os.MkdirAll(folder, 0755)
}

func (d *githubDeployer) LookupServices(names []string) ([]*Service, error) {
	uniqueMap := make(map[string]bool)
	var services []*Service
	for _, name := range names {
		lookupResult, err := d.lookupServicesByTageOrName(name)
		if err != nil {
			return nil, err
		}
		for _, service := range lookupResult {
			if _, exists := uniqueMap[service.Name]; !exists {
				uniqueMap[service.Name] = true
				services = append(services, service)
			}
		}
	}

	if len(services) == 0 {
		return nil, api.NewValidationErr("no services found")
	}

	return services, nil
}

func (d *githubDeployer) lookupServicesByTageOrName(name string) ([]*Service, error) {
	var services []*Service
	for _, service := range d.config.Services {
		serviceName := strings.ToLower(service.Name)
		lookupName := strings.ToLower(name)
		if serviceName == lookupName || slices.ContainsFunc(service.Tags, func(tag string) bool { return strings.ToLower(tag) == lookupName }) {
			currentService := service
			services = append(services, &currentService)
		}
	}

	if len(services) == 0 {
		return nil, api.NewValidationErr(fmt.Sprintf("could not find any service with name or tag of %s", name))
	}

	return services, nil
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

func areServicesFromSameRepo(services []*Service) bool {
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

func areEnvironmentsFromSameBranch(environments []*ServiceEnvironment) bool {
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

func getFreezeFilePath(environment ServiceEnvironment) string {
	if environment.FreezeFilePath != "" {
		return environment.FreezeFilePath
	}

	return environment.TemplatePath
}

type options struct {
	ServiceName string
	Environment string
	Version     string
}
