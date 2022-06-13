package config

import (
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/apono-io/argo-bot/pkg/logging"
	"github.com/apono-io/argo-bot/pkg/slack"
)

type Config struct {
	Deploy  deploy.Config
	Logging logging.Config
	Slack   slack.Config
}
