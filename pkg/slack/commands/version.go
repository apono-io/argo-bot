package commands

import (
	"fmt"
	"github.com/apono-io/argo-bot/pkg/core"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
)

func (c *controller) handleVersion(client *socketmode.Client, cmd slackgo.SlashCommand, _ []string) {
	err := c.sendMessage(client, cmd.ChannelID,
		slackgo.MsgOptionText(fmt.Sprintf("*Argo Bot*\n\tVersion: %s\n\tCommit: %s\n\tBuild Time: %s",
			core.Version, core.Commit, core.BuildDate), false),
		slackgo.MsgOptionResponseURL(cmd.ResponseURL, slackgo.ResponseTypeInChannel),
	)
	if err != nil {
		log.WithError(err).Error("Failed to send message to user")
	}
}
