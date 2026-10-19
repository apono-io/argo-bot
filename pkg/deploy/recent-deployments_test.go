package deploy

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/apono-io/argo-bot/pkg/github"
	"github.com/cristalhq/aconfig"
	"github.com/cristalhq/aconfig/aconfigyaml"
	gh "github.com/google/go-github/v45/github"
)

type fakeGithubClient struct {
	github.Client
	commitsByPath map[string][]*gh.RepositoryCommit
}

func (f *fakeGithubClient) ListCommitsForPath(_ context.Context, _, path string, _ int) ([]*gh.RepositoryCommit, error) {
	return f.commitsByPath[path], nil
}

func repositoryCommit(message string, committedAt time.Time) *gh.RepositoryCommit {
	return &gh.RepositoryCommit{Commit: &gh.Commit{
		Message:   gh.String(message),
		Committer: &gh.CommitAuthor{Date: &committedAt},
	}}
}

func newTestDeployer(commitsByPath map[string][]*gh.RepositoryCommit) *githubDeployer {
	return &githubDeployer{
		config: Config{
			RecentDeploymentWindow: 2 * time.Hour,
			Services: []Service{
				{Name: "app", Environments: []ServiceEnvironment{
					{Name: "dev", GeneratedPath: "generated/dev/app/"},
					{Name: "prod", GeneratedPath: "generated/prod/app/", AllowedBranches: []string{"main"}},
				}},
				{Name: "api", Tags: []string{"backend"}, Environments: []ServiceEnvironment{
					{Name: "dev", GeneratedPath: "generated/dev/api/"},
				}},
				{Name: "worker", Tags: []string{"backend"}, Environments: []ServiceEnvironment{
					{Name: "dev", GeneratedPath: "generated/dev/worker/"},
				}},
			},
		},
		githubClient: &fakeGithubClient{commitsByPath: commitsByPath},
	}
}

func TestListRecentDeploymentsByOthers(t *testing.T) {
	now := time.Now()
	deployer := newTestDeployer(map[string][]*gh.RepositoryCommit{
		"generated/dev/app/": {
			repositoryCommit("Merge branch 'main'", now.Add(-5*time.Minute)),
			repositoryCommit("Deploy app to dev with version 2a7c930 triggered by Jane Doe (jane@example.com) (#27134)\n\nCo-authored-by: Jane Doe <jane@example.com>", now.Add(-40*time.Minute)),
		},
		"generated/prod/app/": {
			repositoryCommit("Deploy app to prod with version 1111111 triggered by Jane Doe (jane@example.com) (#1)", now.Add(-time.Minute)),
		},
		"generated/dev/api/": {
			repositoryCommit("Deploy backend to dev with version 3333333 triggered by Me Myself (ME@example.com) (#2)", now.Add(-time.Minute)),
		},
		"generated/dev/worker/": {
			repositoryCommit("Deploy backend to dev with version 4444444 triggered by John Roe (john@example.com) (#3)", now.Add(-3*time.Hour)),
		},
	})

	t.Run("warns only about recent non-prod deployments by others", func(t *testing.T) {
		recentDeployments, err := deployer.ListRecentDeploymentsByOthers(context.Background(), []string{"app", "backend"}, "dev", "me@example.com")
		if err != nil {
			t.Fatal(err)
		}

		if len(recentDeployments) != 1 {
			t.Fatalf("expected 1 recent deployment, got %+v", recentDeployments)
		}

		recentDeployment := recentDeployments[0]
		if recentDeployment.ServiceName != "app" || recentDeployment.Version != "2a7c930" ||
			recentDeployment.DeployerName != "Jane Doe" || recentDeployment.DeployerEmail != "jane@example.com" {
			t.Fatalf("unexpected recent deployment %+v", recentDeployment)
		}
	})

	t.Run("skips prod environments", func(t *testing.T) {
		recentDeployments, err := deployer.ListRecentDeploymentsByOthers(context.Background(), []string{"app"}, "prod", "me@example.com")
		if err != nil {
			t.Fatal(err)
		}

		if len(recentDeployments) != 0 {
			t.Fatalf("expected no recent deployments on prod, got %+v", recentDeployments)
		}
	})

	t.Run("disabled when the window is 0", func(t *testing.T) {
		deployer.config.RecentDeploymentWindow = 0
		defer func() { deployer.config.RecentDeploymentWindow = 2 * time.Hour }()

		recentDeployments, err := deployer.ListRecentDeploymentsByOthers(context.Background(), []string{"app"}, "dev", "me@example.com")
		if err != nil {
			t.Fatal(err)
		}

		if len(recentDeployments) != 0 {
			t.Fatalf("expected no recent deployments when disabled, got %+v", recentDeployments)
		}
	})
}

func TestRecentDeploymentWindowConfig(t *testing.T) {
	load := func(yaml string) Config {
		configPath := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(configPath, []byte(yaml), 0o600); err != nil {
			t.Fatal(err)
		}

		var cfg struct{ Deploy Config }
		loader := aconfig.LoaderFor(&cfg, aconfig.Config{
			SkipEnv:          true,
			SkipFlags:        true,
			AllowUnknownEnvs: true,
			Files:            []string{configPath},
			FileDecoders:     map[string]aconfig.FileDecoder{".yaml": aconfigyaml.New()},
		})
		if err := loader.Load(); err != nil {
			t.Fatal(err)
		}

		return cfg.Deploy
	}

	baseYaml := "deploy:\n  github:\n    organization: org\n    repository: repo\n    author_email: bot@example.com\n    auth:\n      key_path: key\n      app_id: 1\n      installation_id: 1\n"

	if window := load(baseYaml).RecentDeploymentWindow; window != 2*time.Hour {
		t.Fatalf("expected default window of 2h, got %s", window)
	}

	if window := load(baseYaml + "  recent_deployment_window: 30m\n").RecentDeploymentWindow; window != 30*time.Minute {
		t.Fatalf("expected configured window of 30m, got %s", window)
	}
}
