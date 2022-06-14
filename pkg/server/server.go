package server

import (
	"fmt"
	"github.com/apono-io/argo-bot/pkg/config"
	"github.com/apono-io/argo-bot/pkg/slack"
	"github.com/form3tech-oss/logrus-logzio-hook/pkg/hook"
	"github.com/logzio/logzio-go"
	log "github.com/sirupsen/logrus"
)

func Run(config config.Config) error {
	loggingCfg := config.Logging

	textFormatter := &log.TextFormatter{
		DisableColors: true,
	}

	log.SetFormatter(textFormatter)
	log.SetReportCaller(true)

	if loggingCfg.LogzioListenerAddress != "" && loggingCfg.LogzioLoggingToken != "" {
		sender, err := logzio.New(fmt.Sprintf("%s&type=%s", loggingCfg.LogzioLoggingToken, "argo-bot"),
			logzio.SetUrl(loggingCfg.LogzioListenerAddress),
		)
		if err != nil {
			log.WithError(err).Fatal("Failed to create Logz.io sender")
		}

		logzioHook := hook.NewLogzioHook(sender)
		defer logzioHook.Stop()

		log.AddHook(logzioHook)
	}

	bot, err := slack.New(config.Slack, config.Deploy)
	if err != nil {
		return err
	}

	return bot.Run()
}
