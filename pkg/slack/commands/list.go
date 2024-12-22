package commands

import (
	"fmt"
	"strings"

	"github.com/apono-io/argo-bot/pkg/utils"
	log "github.com/sirupsen/logrus"

	"github.com/shomali11/slacker"
	"github.com/slack-go/slack"
)

func (c *controller) handleList(botCtx slacker.BotContext, request slacker.Request, response slacker.ResponseWriter) {
	ctxLogger := log.WithField("slackUserId", botCtx.Event().UserID).
		WithField("slackChannelId", botCtx.Event().ChannelID)

	servicesArg := request.StringParam("services", "")
	if servicesArg != "" {
		ctxLogger = ctxLogger.WithField("servicesArg", servicesArg)
	}

	serviceNames := c.resolveServiceNames(servicesArg)

	var blocks []slack.Block
	blocks = append(blocks, slack.NewHeaderBlock(
		slack.NewTextBlockObject("plain_text", "Services Status Overview", false, false),
	))

	frozenStatus, err := c.deployer.ListEnvironmentsFrozenStatus(serviceNames)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to get environments frozen status")
		c.sendListErrorMessage(botCtx, ctxLogger, err)
		return
	}

	allServices := c.deployer.ListServices()
	for _, service := range allServices {
		if frozenStatus[service.Name] == nil {
			ctxLogger.WithField("service", service.Name).Error("No frozen status found for service")
			continue
		}

		envs, err := c.deployer.ListEnvironments(service.Name)
		if err != nil {
			ctxLogger.WithError(err).
				WithField("service", service.Name).
				Error("Failed to list environments for service")
			c.sendListErrorMessage(botCtx, ctxLogger, err)
			continue
		}

		var envStatus []string
		for _, env := range envs {
			status := fmt.Sprintf("*%s*: %s",
				env.Name,
				formatFreezeStatus(frozenStatus[service.Name][env.Name]),
			)
			envStatus = append(envStatus, status)
		}

		tagsStr := ""
		if len(service.Tags) > 0 {
			tagsStr = fmt.Sprintf("\nTags: `%s`", strings.Join(service.Tags, "`, `"))
		}

		serviceSection := slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("🔷 *%s*\n%s%s",
					service.Name,
					strings.Join(envStatus, "\n"),
					tagsStr,
				),
				false, false,
			),
			nil, nil,
		)
		blocks = append(blocks, serviceSection)
		blocks = append(blocks, slack.NewDividerBlock())
	}

	err = c.sendListMessage(botCtx, ctxLogger, blocks)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to send message to user")
	}
}

func (c *controller) resolveServiceNames(serviceName string) []string {
	var serviceNames []string

	if serviceName != "" {
		services := utils.UniqueStrings(strings.Split(serviceName, ","))
		serviceNames = c.deployer.ResolveTags(services)
	} else {
		services := c.deployer.ListServices()
		for _, service := range services {
			serviceNames = append(serviceNames, service.Name)
		}
	}

	return serviceNames
}

func (c *controller) sendListMessage(botCtx slacker.BotContext, ctxLogger *log.Entry, blocks []slack.Block) error {
	_, _, _, err := botCtx.SocketModeClient().SendMessage(
		botCtx.Event().ChannelID,
		slack.MsgOptionText("Services Status Overview", false),
		slack.MsgOptionAttachments(slack.Attachment{
			Color: lightBlueColor,
			Blocks: slack.Blocks{
				BlockSet: blocks,
			},
		}),
	)
	return err
}

func (c *controller) sendListErrorMessage(botCtx slacker.BotContext, ctxLogger *log.Entry, executionErr error) {
	errorMsg := fmt.Sprintf("Error: %s", executionErr.Error())
	_, _, _, err := botCtx.SocketModeClient().SendMessage(
		botCtx.Event().ChannelID,
		slack.MsgOptionText("Services Status Overview", false),
		slack.MsgOptionAttachments(slack.Attachment{
			Color: darkRedColor,
			Blocks: slack.Blocks{
				BlockSet: []slack.Block{
					slack.NewSectionBlock(
						slack.NewTextBlockObject("mrkdwn", errorMsg, false, false),
						nil, nil,
					),
				},
			},
		}),
	)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to send error message to user")
	}
}

func formatFreezeStatus(frozen bool) string {
	if frozen {
		return "🔒 Frozen"
	}
	return "✅ Active"
}
