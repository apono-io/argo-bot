package deploy

type Config struct {
	Github   GithubConfig
	Services []Service
}

type GithubConfig struct {
	Auth         GithubAuthConfig `required:"true"`
	Organization string           `required:"true"`
	Repository   string           `required:"true"`
	AuthorName   string           `required:"true" default:"Argo Bot"`
	AuthorEmail  string           `required:"true"`
	CloneTmpDir  string           `required:"true" default:"/tmp"`
}

type GithubAuthConfig struct {
	KeyPath        string `required:"true"`
	AppId          int    `required:"true"`
	InstallationId int    `required:"true"`
}

type Service struct {
	Name               string               `required:"true"`
	GithubOrganization string               `required:"true"`
	GithubRepository   string               `required:"true"`
	Environments       []ServiceEnvironment `required:"true"`
}

type ServiceEnvironment struct {
	Name                 string `required:"true"`
	TemplatePath         string `required:"true"`
	GeneratedPath        string `required:"true"`
	AllowedBranchesCsv   string `default:""`
	DeploymentRepoBranch string `default:""`
}
