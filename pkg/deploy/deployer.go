package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/apono-io/argo-bot/pkg/api"
	"github.com/apono-io/argo-bot/pkg/github"
	gh "github.com/google/go-github/v45/github"
	log "github.com/sirupsen/logrus"
	"gopkg.in/yaml.v3"
)

type FreezeAction string
type ServiceName string
type EnvironmentName string

const (
	FreezeActionFreeze   FreezeAction = "freeze"
	FreezeActionUnfreeze FreezeAction = "unfreeze"
)

const freezeFileName = ".freeze"
const defaultHelmValuesFileName = "argo-bot-values.yaml"

type EnvironmentStatus struct {
	EnvironmentName string
	IsFrozen        bool
}

type Deployer interface {
	GetCommitSha(ctx context.Context, serviceName []string, commit string) (string, string, error)
	Deploy(serviceNames, environmentNames []string, commit, commitUrl, userFullname, userEmail string) (*github.PullRequest, string, error)
	Freeze(serviceNames, environmentNames []string, userFullname, userEmail string, action FreezeAction) (*github.PullRequest, string, error)
	Approve(ctx context.Context, pullRequestId int) error
	Cancel(ctx context.Context, pullRequestId int) error
	ResolveTags(names []string) []string
	ResolveEnvironmentTags(serviceNames, environmentNames []string) []string
	ListServices() []Service
	ListServiceEnvironmentsStatus(serviceNames []string) (map[ServiceName][]EnvironmentStatus, error)
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

func (d *githubDeployer) Deploy(serviceNames, environmentNames []string, commit, commitUrl, userFullname, userEmail string) (*github.PullRequest, string, error) {
	ctx := context.Background()
	logWithCtx := log.WithFields(log.Fields{
		"environments": environmentNames,
		"serviceNames": serviceNames,
		"commit":       commit,
	})

	targets, deploymentBranch, err := d.resolveTargets(serviceNames, environmentNames)
	if err != nil {
		return nil, "", err
	}

	servicesString := strings.Join(serviceNames, ",")
	environmentsString := strings.Join(environmentNames, ",")
	branch := fmt.Sprintf("deploy-%s-%s", servicesString, environmentsString)

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

	var frozenTargets []string
	for _, target := range targets {
		if len(target.Environment.AllowedBranches) > 0 {
			logWithCtx.Infof("Validating branch")
			validBranch, err := d.validateBranch(ctx, target.Service.GithubOrganization, target.Service.GithubRepository, commit, target.Environment.AllowedBranches)
			if err != nil {
				return nil, "", err
			}

			if !validBranch {
				return nil, "", api.NewValidationErr(fmt.Sprintf("commit is not in allowed branches for service %s", target.Service.Name))
			}
		}

		freezeFilePath := getFreezeFilePath(*target.Environment)
		frozen, err := d.checkIfServiceFrozen(baseFolder, freezeFilePath)
		if err != nil {
			return nil, "", fmt.Errorf("failed to check if %s is frozen, error: %w", target, err)
		}
		if frozen {
			frozenTargets = append(frozenTargets, target.String())
		}
	}

	if len(frozenTargets) > 0 {
		return nil, "", api.NewValidationErr(fmt.Sprintf("cannot deploy: services are frozen: %s", strings.Join(frozenTargets, ", ")))
	}

	logWithCtx.Infof("Starting deployment")
	prTitle := fmt.Sprintf("Deploy %s to %s with version %s triggered by %s (%s)", servicesString, environmentsString, commit[:7], userFullname, userEmail)

	fileOwners := make(map[string]string)
	var uniqueFiles []string
	for _, target := range targets {
		files, err := d.renderTemplates(baseFolder, target.Service.Name, commit, target.Environment, logWithCtx)
		if err != nil {
			return nil, "", fmt.Errorf("failed to render templates for %s, error: %w", target, err)
		}

		for _, file := range files {
			if existingOwner, exists := fileOwners[file]; exists {
				return nil, "", api.NewValidationErr(fmt.Sprintf("file conflict: both %s and %s are trying to modify file '%s'", existingOwner, target, file))
			}
			fileOwners[file] = target.String()
			uniqueFiles = append(uniqueFiles, file)
		}
	}

	tree, err := d.githubClient.CreateTree(ctx, ref, baseFolder, uniqueFiles)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create diff tree for services, error: %w", err)
	}

	if err = d.githubClient.PushCommit(ctx, ref, tree, userFullname, userEmail, withTargetsList(prTitle, targets)); err != nil {
		return nil, "", fmt.Errorf("failed to create commit for services, error: %w", err)
	}

	prDescription := fmt.Sprintf("Service Names: %s\nEnvironment: %s\nCommit: [%s](%s)\nRequested by: %s (%s)\n\nDeployments:\n%s",
		servicesString, environmentsString, commit[:7], commitUrl, userFullname, userEmail, targetsList(targets))
	pr, diff, err := d.githubClient.CreatePR(ctx, prTitle, prDescription, deploymentBranch, branch)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create pull request, error: %w", err)
	}

	logWithCtx.Infof("Created pull request for deployment")

	return pr, diff, nil
}

func (d *githubDeployer) Freeze(serviceNames, environmentNames []string, userFullname, userEmail string, action FreezeAction) (*github.PullRequest, string, error) {
	ctx := context.Background()
	logWithCtx := log.WithFields(log.Fields{
		"environments": environmentNames,
		"serviceNames": serviceNames,
		"action":       action,
	})

	targets, deploymentBranch, err := d.resolveTargets(serviceNames, environmentNames)
	if err != nil {
		return nil, "", err
	}

	servicesString := strings.Join(serviceNames, ",")
	environmentsString := strings.Join(environmentNames, ",")

	logWithCtx.Infof("Starting %s operation", action)
	branch := fmt.Sprintf("%s-%s-%s", action, servicesString, environmentsString)
	prTitle := fmt.Sprintf("%s %s to %s triggered by %s (%s)", action, servicesString, environmentsString, userFullname, userEmail)

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

	var changedTargets []deploymentTarget
	var allFreezeFiles []string
	seenFreezeFiles := make(map[string]struct{})

	for _, target := range targets {
		freezeFilePath := getFreezeFilePath(*target.Environment)

		var freezeFile string
		if action == FreezeActionUnfreeze {
			frozen, err := d.checkIfServiceFrozen(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to check if %s is frozen, error: %w", target, err)
			}
			if !frozen {
				continue
			}
			freezeFile, err = d.removeFreezeFile(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to remove freeze file for %s, error: %w", target, err)
			}
		} else {
			freezeFile, err = d.createFreezeFile(baseFolder, freezeFilePath)
			if err != nil {
				return nil, "", fmt.Errorf("failed to create freeze file for %s, error: %w", target, err)
			}
		}

		if _, seen := seenFreezeFiles[freezeFile]; !seen {
			seenFreezeFiles[freezeFile] = struct{}{}
			allFreezeFiles = append(allFreezeFiles, freezeFile)
		}
		changedTargets = append(changedTargets, target)
	}

	if len(changedTargets) == 0 {
		logWithCtx.Info("No changes needed - services already in desired state")
		return nil, "", nil
	}

	tree, err := d.githubClient.CreateTree(ctx, ref, baseFolder, allFreezeFiles)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create diff tree for %s operation, error: %w", action, err)
	}

	commitMsg := fmt.Sprintf("%s %s on %s triggered by %s (%s)", action, servicesString, environmentsString, userFullname, userEmail)
	if err = d.githubClient.PushCommit(ctx, ref, tree, userFullname, userEmail, withTargetsList(commitMsg, changedTargets)); err != nil {
		return nil, "", fmt.Errorf("failed to create commit for %s operation, error: %w", action, err)
	}

	prDescription := fmt.Sprintf("Service Names: %s\nEnvironment: %s\nRequested by: %s (%s)\n\n%s:\n%s",
		servicesString, environmentsString, userFullname, userEmail, action, targetsList(changedTargets))
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

func (d *githubDeployer) resolveTargets(serviceNames, environmentNames []string) ([]deploymentTarget, string, error) {
	services, err := d.LookupServices(serviceNames)
	if err != nil {
		return nil, "", err
	}

	var targets []deploymentTarget
	var environments []*ServiceEnvironment
	for _, service := range services {
		serviceEnvironments, err := d.LookupEnvironments(service, environmentNames)
		if err != nil {
			return nil, "", err
		}

		for _, environment := range serviceEnvironments {
			targets = append(targets, deploymentTarget{Service: service, Environment: environment})
			environments = append(environments, environment)
		}
	}

	if !areEnvironmentsFromSameBranch(environments) {
		return nil, "", api.NewValidationErr("environments have different deployment branches")
	}

	if err = validateNoSharedGeneratedPath(targets); err != nil {
		return nil, "", err
	}

	deploymentBranch := environments[0].DeploymentRepoBranch

	return targets, deploymentBranch, nil
}

// Rendering clears the generated folder first, so targets sharing one would
// silently discard each other's manifests.
func validateNoSharedGeneratedPath(targets []deploymentTarget) error {
	owners := make(map[string]string, len(targets))
	for _, target := range targets {
		owner := fmt.Sprintf("%s/%s", target.Service.Name, target.Environment.Name)
		if existingOwner, exists := owners[target.Environment.GeneratedPath]; exists {
			return api.NewValidationErr(fmt.Sprintf("%s and %s both generate into %s, only one of them can be deployed at a time",
				existingOwner, owner, target.Environment.GeneratedPath))
		}
		owners[target.Environment.GeneratedPath] = owner
	}

	return nil
}

func (d *githubDeployer) validateBranch(ctx context.Context, organization, repository, commit string, branches []string) (bool, error) {
	return d.githubClient.CommitInBranch(ctx, organization, repository, commit, branches)
}

func (d *githubDeployer) checkIfServiceFrozen(baseFolder, freezeFilePath string) (bool, error) {
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

func (d *githubDeployer) renderTemplates(baseFolder, serviceName, commit string, env *ServiceEnvironment, log *log.Entry) ([]string, error) {
	templatePath := env.TemplatePath
	generatedPath := env.GeneratedPath
	environment := env.Name

	// Get existing files before cleaning
	existingFiles := d.findExistingFiles(baseFolder, generatedPath)
	log.Infof("Found %d existing files in %s", len(existingFiles), generatedPath)
	for _, f := range existingFiles {
		log.Debugf("Existing file: %s", f)
	}

	generatedFolder := filepath.Join(baseFolder, generatedPath)
	err := d.cleanFolder(generatedFolder)
	if err != nil {
		return nil, err
	}

	templateFolder := filepath.Join(baseFolder, templatePath)

	var newFiles []string
	if d.isHelmChart(templateFolder) {
		log.Infof("Processing Helm chart templates for service %s", serviceName)
		newFiles, err = d.processHelmChart(baseFolder, templateFolder, generatedFolder, serviceName, environment, commit, env)
		// log the files generated by Helm chart processing
		for _, file := range newFiles {
			log.Infof("Generated Helm file: %s", file)
		}
	} else {
		log.Infof("Processing Go templates for service %s", serviceName)
		newFiles, err = d.processGoTemplates(baseFolder, templateFolder, generatedFolder, serviceName, environment, commit)
	}

	if err != nil {
		return nil, err
	}

	// Add deletion markers for files that no longer exist
	return d.addDeletionMarkers(existingFiles, newFiles), nil
}

func (d *githubDeployer) addDeletionMarkers(existingFiles, newFiles []string) []string {
	newFileSet := make(map[string]bool)
	for _, file := range newFiles {
		newFileSet[file] = true
	}

	allFiles := make([]string, len(newFiles))
	copy(allFiles, newFiles)

	for _, existingFile := range existingFiles {
		if !newFileSet[existingFile] {
			allFiles = append(allFiles, existingFile)
		}
	}

	return allFiles
}

func (d *githubDeployer) isHelmChart(templateFolder string) bool {
	chartPath := filepath.Join(templateFolder, "Chart.yaml")
	_, err := os.Stat(chartPath)
	return err == nil
}

func (d *githubDeployer) processHelmChart(baseFolder, templateFolder, generatedFolder, serviceName, environment, commit string, env *ServiceEnvironment) ([]string, error) {
	copiedFiles, err := d.copyDirectory(baseFolder, templateFolder, generatedFolder)
	if err != nil {
		return nil, err
	}

	valuesFile, err := d.createArgoBotValuesFile(baseFolder, generatedFolder, serviceName, environment, commit, env)
	if err != nil {
		return nil, err
	}

	// Check if valuesFile already exists in copiedFiles and replace it if found
	found := false
	for i, file := range copiedFiles {
		if file == valuesFile {
			found = true
			copiedFiles[i] = valuesFile // Replace (though it's the same path)
			break
		}
	}

	// Only append if not already in the list
	if !found {
		copiedFiles = append(copiedFiles, valuesFile)
	}

	return copiedFiles, nil
}

func (d *githubDeployer) processGoTemplates(baseFolder, templateFolder, generatedFolder, serviceName, environment, commit string) ([]string, error) {
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

func (d *githubDeployer) copyDirectory(baseFolder, sourceFolder, destFolder string) ([]string, error) {
	var copiedFiles []string

	err := filepath.Walk(sourceFolder, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}

		relPath, err := filepath.Rel(sourceFolder, path)
		if err != nil {
			return err
		}

		destPath := filepath.Join(destFolder, relPath)

		if info.IsDir() {
			return os.MkdirAll(destPath, info.Mode())
		}

		if err := d.copyFile(path, destPath); err != nil {
			return err
		}

		fileRelPath, err := filepath.Rel(baseFolder, destPath)
		if err != nil {
			return err
		}
		copiedFiles = append(copiedFiles, fileRelPath)

		return nil
	})

	return copiedFiles, err
}

func (d *githubDeployer) copyFile(src, dest string) error {
	srcFile, err := os.Open(src)
	if err != nil {
		return err
	}
	defer srcFile.Close()

	destFile, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer destFile.Close()

	_, err = io.Copy(destFile, srcFile)
	return err
}

func (d *githubDeployer) createArgoBotValuesFile(baseFolder, generatedFolder, serviceName, environment, commit string, env *ServiceEnvironment) (string, error) {
	targetFileName := defaultHelmValuesFileName
	if env.HelmValuesTargetFile != "" {
		targetFileName = env.HelmValuesTargetFile
	}

	valuesPath := filepath.Join(generatedFolder, targetFileName)

	argoBotValues := map[string]interface{}{
		"argoBot": map[string]interface{}{
			"serviceName": serviceName,
			"environment": environment,
			"version":     commit,
		},
	}

	argoBotYAML, err := yaml.Marshal(argoBotValues)
	if err != nil {
		return "", fmt.Errorf("failed to marshal argoBot values: %w", err)
	}
	argoBotSection := strings.TrimRight(string(argoBotYAML), "\n")

	existingContent, err := os.ReadFile(valuesPath)
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("failed to read existing values file: %w", err)
	}

	var finalContent string

	if len(existingContent) > 0 {
		var values map[string]interface{}
		err = yaml.Unmarshal(existingContent, &values)
		if err != nil {
			return "", fmt.Errorf("failed to parse existing values file: %w", err)
		}

		if _, hasArgoBot := values["argoBot"]; hasArgoBot {
			return "", fmt.Errorf("values file must not contain argoBot section as it is auto-generated by argo-bot")
		}

		contentStr := string(existingContent)
		finalContent = strings.TrimRight(contentStr, "\n") + "\n\n" + argoBotSection + "\n"
	} else {
		finalContent = argoBotSection + "\n"
	}

	err = os.WriteFile(valuesPath, []byte(finalContent), 0644)
	if err != nil {
		return "", err
	}

	return filepath.Rel(baseFolder, valuesPath)
}

func (d *githubDeployer) findExistingFiles(baseFolder, generatedPath string) []string {
	var files []string
	fullPath := filepath.Join(baseFolder, generatedPath)

	err := filepath.Walk(fullPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(baseFolder, path)
		if err != nil {
			return nil
		}

		files = append(files, relPath)
		return nil
	})

	if err != nil {
		return []string{}
	}

	return files
}

func (d *githubDeployer) createFreezeFile(baseFolder, freezeFilePath string) (string, error) {
	freezeFile := filepath.Join(baseFolder, freezeFilePath, freezeFileName)
	file, err := os.Create(freezeFile)
	if err != nil {
		return "", err
	}
	defer file.Close()

	warning := "# This file is managed by the GitOps deployment bot.\n# DO NOT EDIT OR DELETE THIS FILE MANUALLY.\n# Use the bot commands to manage service freezes."
	if _, err := file.WriteString(warning); err != nil {
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

	frozen, err := d.checkIfServiceFrozen(baseFolder, freezeFilePath)
	if err != nil {
		return "", err
	}
	if !frozen {
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

func (d *githubDeployer) LookupEnvironments(service *Service, names []string) ([]*ServiceEnvironment, error) {
	uniqueMap := make(map[string]bool)
	var environments []*ServiceEnvironment
	for _, environment := range service.Environments {
		if !environmentMatchesAny(environment, names) {
			continue
		}

		if uniqueMap[environment.Name] {
			continue
		}

		uniqueMap[environment.Name] = true
		currentEnvironment := environment
		environments = append(environments, &currentEnvironment)
	}

	if len(environments) == 0 {
		return nil, api.NewValidationErr(fmt.Sprintf("environment %s does not exist for service %s", strings.Join(names, ","), service.Name))
	}

	return environments, nil
}

func environmentMatchesAny(environment ServiceEnvironment, names []string) bool {
	for _, name := range names {
		lookupName := strings.ToLower(name)
		if strings.ToLower(environment.Name) == lookupName {
			return true
		}
		if slices.ContainsFunc(environment.Tags, func(tag string) bool { return strings.ToLower(tag) == lookupName }) {
			return true
		}
	}

	return false
}

func (d *githubDeployer) ResolveEnvironmentTags(serviceNames, environmentNames []string) []string {
	services, err := d.LookupServices(serviceNames)
	if err != nil {
		return environmentNames
	}

	uniqueMap := make(map[string]bool)
	var resolvedNames []string
	for _, service := range services {
		environments, err := d.LookupEnvironments(service, environmentNames)
		if err != nil {
			continue
		}

		for _, environment := range environments {
			if uniqueMap[environment.Name] {
				continue
			}
			uniqueMap[environment.Name] = true
			resolvedNames = append(resolvedNames, environment.Name)
		}
	}

	if len(resolvedNames) == 0 {
		return environmentNames
	}

	return resolvedNames
}

func (d *githubDeployer) ListServiceEnvironmentsStatus(serviceNames []string) (map[ServiceName][]EnvironmentStatus, error) {
	services, err := d.LookupServices(serviceNames)
	if err != nil {
		return nil, err
	}

	branchEnvironments := make(map[string][]serviceEnvToCheck)
	for _, service := range services {
		for _, env := range service.Environments {
			branchEnvironments[env.DeploymentRepoBranch] = append(
				branchEnvironments[env.DeploymentRepoBranch],
				serviceEnvToCheck{
					ServiceName:    service.Name,
					Environment:    env,
					FreezeFilePath: getFreezeFilePath(env),
				},
			)
		}
	}

	serviceToEnvStatuses := make(map[ServiceName][]EnvironmentStatus)

	for branch, environments := range branchEnvironments {
		serviceToEnvWithStatus, err := d.getEnvironmentsStatusForBranch(branch, environments)
		if err != nil {
			return nil, err
		}

		for service, envToFreezeStatus := range serviceToEnvWithStatus {
			envStatuses := make([]EnvironmentStatus, 0, len(envToFreezeStatus))
			for env, isFrozen := range envToFreezeStatus {
				envStatuses = append(envStatuses, EnvironmentStatus{
					EnvironmentName: string(env),
					IsFrozen:        isFrozen,
				})
			}

			serviceToEnvStatuses[service] = envStatuses
		}
	}

	return serviceToEnvStatuses, nil
}

func (d *githubDeployer) getEnvironmentsStatusForBranch(branch string, environments []serviceEnvToCheck) (map[ServiceName]map[EnvironmentName]bool, error) {
	baseFolder, _, err := d.cloneBranch(context.Background(), "check-freeze-status", branch)
	if err != nil {
		return nil, fmt.Errorf("failed to clone repository for branch %s: %w", branch, err)
	}

	defer func() {
		err := os.RemoveAll(baseFolder)
		if err != nil {
			log.WithError(err).Error("failed to remove source folder")
		}
	}()

	frozenStatus := make(map[ServiceName]map[EnvironmentName]bool)

	for _, env := range environments {
		frozen, err := d.checkIfServiceFrozen(baseFolder, env.FreezeFilePath)
		if err != nil {
			return nil, fmt.Errorf(
				"failed to check freeze status for service %s environment %s: %w",
				env.ServiceName, env.Environment.Name, err,
			)
		}

		serviceName := ServiceName(env.ServiceName)
		envName := EnvironmentName(env.Environment.Name)
		if frozenStatus[serviceName] == nil {
			frozenStatus[serviceName] = make(map[EnvironmentName]bool)
		}

		frozenStatus[serviceName][envName] = frozen
	}

	return frozenStatus, nil
}

type deploymentTarget struct {
	Service     *Service
	Environment *ServiceEnvironment
}

func (t deploymentTarget) String() string {
	return fmt.Sprintf("%s/%s", t.Service.Name, t.Environment.Name)
}

func targetsList(targets []deploymentTarget) string {
	lines := make([]string, 0, len(targets))
	for _, target := range targets {
		lines = append(lines, "- "+target.String())
	}

	return strings.Join(lines, "\n")
}

// The commit body is what carries a tag's expansion through the squash merge.
func withTargetsList(message string, targets []deploymentTarget) string {
	if len(targets) < 2 {
		return message
	}

	return fmt.Sprintf("%s\n\n%s", message, targetsList(targets))
}

type serviceEnvToCheck struct {
	ServiceName    string
	Environment    ServiceEnvironment
	FreezeFilePath string
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

func (d *githubDeployer) ListServices() []Service {
	return d.config.Services
}
