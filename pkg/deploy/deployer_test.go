package deploy

import (
	"testing"

	"github.com/apono-io/argo-bot/pkg/api"
)

func testConfig() Config {
	return Config{
		Services: []Service{
			{
				Name:               "api",
				GithubOrganization: "apono-io",
				GithubRepository:   "monorepo",
				Tags:               []string{"all-backend"},
				Environments: []ServiceEnvironment{
					{Name: "prod-us", Tags: []string{"all-prod"}, TemplatePath: "templates/api", GeneratedPath: "generated/prod-us/api"},
					{Name: "prod-eu", Tags: []string{"all-prod"}, TemplatePath: "templates/api", GeneratedPath: "generated/prod-eu/api"},
					{Name: "dev", TemplatePath: "templates/api", GeneratedPath: "generated/dev/api"},
				},
			},
			{
				Name:               "worker",
				GithubOrganization: "apono-io",
				GithubRepository:   "monorepo",
				Tags:               []string{"all-backend"},
				Environments: []ServiceEnvironment{
					{Name: "prod-us", Tags: []string{"all-prod"}, TemplatePath: "templates/worker", GeneratedPath: "generated/prod-us/worker"},
					{Name: "dev", TemplatePath: "templates/worker", GeneratedPath: "generated/dev/worker"},
				},
			},
		},
	}
}

func testDeployer(config Config) *githubDeployer {
	return &githubDeployer{config: config}
}

func envNames(environments []*ServiceEnvironment) []string {
	names := make([]string, 0, len(environments))
	for _, environment := range environments {
		names = append(names, environment.Name)
	}
	return names
}

func targetNames(targets []deploymentTarget) []string {
	names := make([]string, 0, len(targets))
	for _, target := range targets {
		names = append(names, target.Service.Name+"/"+target.Environment.Name)
	}
	return names
}

func assertEqual(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestLookupEnvironmentsByName(t *testing.T) {
	d := testDeployer(testConfig())
	service := &d.config.Services[0]

	environments, err := d.LookupEnvironments(service, []string{"prod-us"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, envNames(environments), []string{"prod-us"})
}

func TestLookupEnvironmentsByTagExpandsToEveryMatchingEnvironment(t *testing.T) {
	d := testDeployer(testConfig())
	service := &d.config.Services[0]

	environments, err := d.LookupEnvironments(service, []string{"all-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, envNames(environments), []string{"prod-us", "prod-eu"})
}

func TestLookupEnvironmentsIsCaseInsensitive(t *testing.T) {
	d := testDeployer(testConfig())
	service := &d.config.Services[0]

	environments, err := d.LookupEnvironments(service, []string{"ALL-PROD"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, envNames(environments), []string{"prod-us", "prod-eu"})
}

func TestLookupEnvironmentsExpandsOnlyEnvironmentsTheServiceHas(t *testing.T) {
	d := testDeployer(testConfig())
	worker := &d.config.Services[1]

	environments, err := d.LookupEnvironments(worker, []string{"all-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, envNames(environments), []string{"prod-us"})
}

func TestLookupEnvironmentsDedupesOverlapBetweenNameAndTag(t *testing.T) {
	d := testDeployer(testConfig())
	service := &d.config.Services[0]

	environments, err := d.LookupEnvironments(service, []string{"prod-us", "all-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, envNames(environments), []string{"prod-us", "prod-eu"})
}

func TestLookupEnvironmentsReturnsValidationErrorWhenNothingMatches(t *testing.T) {
	d := testDeployer(testConfig())
	service := &d.config.Services[0]

	_, err := d.LookupEnvironments(service, []string{"staging"})
	if err == nil {
		t.Fatal("expected an error for an environment the service does not have")
	}
	if _, ok := err.(api.ValidationErr); !ok {
		t.Fatalf("expected a validation error, got %T: %v", err, err)
	}
}

func TestResolveTargetsFansOutAcrossServicesAndEnvironments(t *testing.T) {
	d := testDeployer(testConfig())

	targets, _, err := d.resolveTargets([]string{"all-backend"}, []string{"all-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, targetNames(targets), []string{"api/prod-us", "api/prod-eu", "worker/prod-us"})
}

func TestResolveTargetsAcceptsMultipleEnvironmentNames(t *testing.T) {
	d := testDeployer(testConfig())

	targets, _, err := d.resolveTargets([]string{"api"}, []string{"prod-us", "dev"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertEqual(t, targetNames(targets), []string{"api/prod-us", "api/dev"})
}

func TestResolveTargetsRejectsEnvironmentsOnDifferentDeploymentBranches(t *testing.T) {
	config := testConfig()
	config.Services[0].Environments[1].DeploymentRepoBranch = "release"

	_, _, err := testDeployer(config).resolveTargets([]string{"api"}, []string{"all-prod"})
	if err == nil {
		t.Fatal("expected an error when an alias spans different deployment branches")
	}
	if _, ok := err.(api.ValidationErr); !ok {
		t.Fatalf("expected a validation error, got %T: %v", err, err)
	}
}

func TestResolveTargetsRejectsTargetsSharingAGeneratedPath(t *testing.T) {
	config := testConfig()
	config.Services[0].Environments[1].GeneratedPath = config.Services[0].Environments[0].GeneratedPath

	_, _, err := testDeployer(config).resolveTargets([]string{"api"}, []string{"all-prod"})
	if err == nil {
		t.Fatal("expected an error when two targets share a generatedPath")
	}
	if _, ok := err.(api.ValidationErr); !ok {
		t.Fatalf("expected a validation error, got %T: %v", err, err)
	}
}

func TestResolveTargetsReturnsSharedDeploymentBranch(t *testing.T) {
	config := testConfig()
	config.Services[0].Environments[0].DeploymentRepoBranch = "release"
	config.Services[0].Environments[1].DeploymentRepoBranch = "release"

	_, branch, err := testDeployer(config).resolveTargets([]string{"api"}, []string{"all-prod"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "release" {
		t.Fatalf("got deployment branch %q, want %q", branch, "release")
	}
}

func TestResolveTargetsOrderFollowsConfigOrder(t *testing.T) {
	d := testDeployer(testConfig())

	// Repeat: Go randomises map iteration, so a single pass can pass by luck.
	for i := 0; i < 20; i++ {
		targets, _, err := d.resolveTargets([]string{"all-backend"}, []string{"all-prod"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		assertEqual(t, targetNames(targets), []string{"api/prod-us", "api/prod-eu", "worker/prod-us"})
	}
}

func TestResolveEnvironmentTagsReturnsResolvedEnvironmentNames(t *testing.T) {
	d := testDeployer(testConfig())

	assertEqual(t, d.ResolveEnvironmentTags([]string{"all-backend"}, []string{"all-prod"}), []string{"prod-us", "prod-eu"})
}

func TestResolveEnvironmentTagsFallsBackToInputWhenNothingResolves(t *testing.T) {
	d := testDeployer(testConfig())

	assertEqual(t, d.ResolveEnvironmentTags([]string{"api"}, []string{"staging"}), []string{"staging"})
}
