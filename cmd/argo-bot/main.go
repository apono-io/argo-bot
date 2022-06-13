package main

import (
	"github.com/apono-io/argo-bot/pkg/config"
	"github.com/apono-io/argo-bot/pkg/server"
	"github.com/cristalhq/aconfig"
	"github.com/cristalhq/aconfig/aconfigdotenv"
	"github.com/cristalhq/aconfig/aconfigyaml"
)

func main() {
	var cfg config.Config
	loader := aconfig.LoaderFor(&cfg, aconfig.Config{
		Files: []string{"/var/opt/argo-bot/config.yaml", "argo-bot.yaml", ".env"},
		FileDecoders: map[string]aconfig.FileDecoder{
			".env":  aconfigdotenv.New(),
			".yaml": aconfigyaml.New(),
		},
		MergeFiles: true,
	})

	err := loader.Load()
	if err != nil {
		panic(err)
	}

	err = server.Run(cfg)
	if err != nil {
		panic(err)
	}
}
