package github

type Config struct {
	Auth         AuthConfig `required:"true"`
	Organization string     `required:"true"`
	Repository   string     `required:"true"`
	AuthorName   string     `required:"true" default:"Argo Bot"`
	AuthorEmail  string     `required:"true"`
	CloneTmpDir  string     `required:"true" default:"/tmp"`
}

type AuthConfig struct {
	KeyPath        string `required:"true"`
	AppId          int    `required:"true"`
	InstallationId int    `required:"true"`
}
