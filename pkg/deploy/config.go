package deploy

type Config struct {
	Github   GithubConfig
	Services []Service
}

type GithubConfig struct {
	Token        string `required:"true"`
	Organization string `required:"true"`
	Repository   string `required:"true"`
	AuthorName   string `required:"true" default:"Argo Bot"`
	AuthorEmail  string `required:"true" yaml:"author_email"`
	CloneTmpDir  string `required:"true" default:"/tmp"`
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
