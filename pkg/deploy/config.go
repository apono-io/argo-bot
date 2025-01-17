package deploy

import "github.com/apono-io/argo-bot/pkg/github"

type Config struct {
	Github   github.Config
	Services []Service
}

type Service struct {
	Name               string               `required:"true"`
	GithubOrganization string               `required:"true"`
	GithubRepository   string               `required:"true"`
	Environments       []ServiceEnvironment `required:"true"`
	Tags               []string
}

type ServiceEnvironment struct {
	Name                 string `required:"true"`
	TemplatePath         string `required:"true"`
	GeneratedPath        string `required:"true"`
	AllowedBranches      []string
	DeploymentRepoBranch string `default:""`
	FreezeFilePath       string `default:""`
}
