package commands

import (
	"fmt"
	"github.com/apono-io/argo-bot/pkg/deploy"
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

	serviceNamesToList := c.resolveServiceNames(servicesArg)

	var blocks []slack.Block
	blocks = append(blocks, slack.NewHeaderBlock(
		slack.NewTextBlockObject("plain_text", "Services Status Overview", false, false),
	))

	serviceToEnvStatuses, err := c.deployer.ListServiceEnvironmentsStatus(serviceNamesToList)
	if err != nil {
		ctxLogger.WithError(err).Error("Failed to get environments frozen status")
		c.sendListErrorMessage(botCtx, ctxLogger, err)
		return
	}

	serviceNameToConfig := make(map[string]deploy.Service)
	for _, serviceConfig := range c.deployer.ListServices() {
		serviceNameToConfig[serviceConfig.Name] = serviceConfig
	}

	for _, service := range serviceNamesToList {
		serviceConfig := serviceNameToConfig[service]
		envStatuses := serviceToEnvStatuses[deploy.ServiceName(service)]

		if envStatuses == nil {
			ctxLogger.WithField("service", service).Error("No environments statuses found for service")
			continue
		}

		var envStatusStrings []string
		for _, envStatus := range envStatuses {
			status := fmt.Sprintf("*%s*: %s", envStatus.EnvironmentName, formatFreezeStatus(envStatus.IsFrozen))
			envStatusStrings = append(envStatusStrings, status)
		}

		tagsStr := ""
		if len(serviceConfig.Tags) > 0 {
			tagsStr = fmt.Sprintf("\nTags: `%s`", strings.Join(serviceConfig.Tags, "`, `"))
		}

		serviceSection := slack.NewSectionBlock(
			slack.NewTextBlockObject("mrkdwn",
				fmt.Sprintf("🔷 *%s*\n%s%s",
					serviceConfig.Name,
					strings.Join(envStatusStrings, "\n"),
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
