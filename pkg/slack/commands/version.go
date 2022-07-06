package commands

import (
	"fmt"
	"github.com/apono-io/argo-bot/pkg/core"
	"github.com/shomali11/slacker"
	log "github.com/sirupsen/logrus"
)

func (c *controller) handleVersion(_ slacker.BotContext, _ slacker.Request, response slacker.ResponseWriter) {
	err := response.Reply(fmt.Sprintf("*Argo Bot*\n\tVersion: %s\n\tCommit: %s\n\tBuild Time: %s",
		core.Version, core.Commit, core.BuildDate))
	if err != nil {
		log.WithError(err).Error("Failed to send message to user")
	}
}
