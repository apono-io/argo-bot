package commands

import (
	"github.com/apono-io/argo-bot/pkg/deploy"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	"strings"
)

func RegisterCommandHandlers(eventHandler *socketmode.SocketmodeHandler, deployer deploy.Deployer) {
	ctrl := controller{
		deployer: deployer,
		handlers: make(map[string]CommandHandler),
	}

	ctrl.init()
	eventHandler.HandleSlashCommand(
		"/argo",
		ctrl.handleSlashCommand,
	)

	eventHandler.HandleInteractionBlockAction(
		deploymentApproveActionId,
		ctrl.handleApproval,
	)

	eventHandler.HandleInteractionBlockAction(
		deploymentDenyActionId,
		ctrl.handleApproval,
	)
}

type CommandHandler func(client *socketmode.Client, cmd slackgo.SlashCommand, args []string)

type controller struct {
	deployer deploy.Deployer
	handlers map[string]CommandHandler
}

func (c *controller) init() {
	c.registerSubCommand("deploy", c.handleDeploy)
	c.registerSubCommand("version", c.handleVersion)
}

func (c *controller) registerSubCommand(subCommandName string, handler CommandHandler) {
	c.handlers[subCommandName] = handler
}

func (c *controller) handleSlashCommand(evt *socketmode.Event, client *socketmode.Client) {
	client.Ack(*evt.Request)

	slashCommandEvent, _ := evt.Data.(slackgo.SlashCommand)
	text := slashCommandEvent.Text
	parts := strings.Split(text, " ")

	subCommand := parts[0]
	if handler, exists := c.handlers[subCommand]; exists {
		handler(client, slashCommandEvent, parts[1:])
	} else {

	}
}

func (c *controller) sendMessage(client *socketmode.Client, channel string, options ...slackgo.MsgOption) error {
	_, _, _, err := client.SendMessage(channel, options...)
	return err
}
