package slack

import (
	"context"
	"fmt"
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/apono-io/argo-bot/pkg/slack/commands"
	"github.com/sbstjn/allot"
	"github.com/shomali11/commander"
	"github.com/shomali11/proper"
	"github.com/shomali11/slacker"
	log "github.com/sirupsen/logrus"
	slackgo "github.com/slack-go/slack"
	"github.com/slack-go/slack/socketmode"
	stdlog "log"
)

type Bot interface {
	Run() error
}

func New(config Config, deployConfig deploy.Config) (Bot, error) {
	client := slackgo.New(config.BotToken,
		slackgo.OptionAppLevelToken(config.AppToken),
		slackgo.OptionLog(stdlog.New(log.StandardLogger().Out, "api: ", stdlog.Lshortfile|stdlog.LstdFlags)),
	)

	socketmodeClient := socketmode.New(client,
		socketmode.OptionLog(stdlog.New(log.StandardLogger().Out, "socketmode: ", stdlog.Lshortfile|stdlog.LstdFlags)),
	)

	slackerBot := slacker.NewClient(config.BotToken, config.AppToken, slacker.WithDebug(true))
	return &bot{
		client:           client,
		socketmodeClient: socketmodeClient,
		deployConfig:     deployConfig,
		slackerBot:       slackerBot,
	}, nil
}

type bot struct {
	client           *slackgo.Client
	socketmodeClient *socketmode.Client
	deployConfig     deploy.Config
	slackerBot       *slacker.Slacker
}

func (b *bot) Run() error {
	b.slackerBot.CustomCommand(func(usage string, definition *slacker.CommandDefinition) slacker.BotCommand {
		return &cmd{
			usage:      usage,
			definition: definition,
			command:    allot.New(fmt.Sprintf("argo-local %s", usage)),
		}
	})

	d, err := deploy.New(b.deployConfig)
	if err != nil {
		return err
	}

	commands.RegisterCommandHandlers(b.slackerBot, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return b.slackerBot.Listen(ctx)
}

type cmd struct {
	usage      string
	definition *slacker.CommandDefinition
	command    allot.Command
	tokens     []*commander.Token
}

func (c *cmd) Usage() string {
	return c.usage
}

func (c *cmd) Definition() *slacker.CommandDefinition {
	return c.definition
}

func (c *cmd) Match(text string) (*proper.Properties, bool) {
	match, err := c.command.Match(text)
	if err != nil {
		return nil, false
	}

	m := make(map[string]string)
	for _, param := range c.command.Parameters() {
		val, _ := match.Parameter(param)
		m[param.Name()] = val
	}

	return proper.NewProperties(m), true
}

func (c *cmd) Tokenize() []*commander.Token {
	return c.tokens
}

func (c *cmd) Execute(botCtx slacker.BotContext, request slacker.Request, response slacker.ResponseWriter) {
	log.Printf("Executing command [%s] invoked by %s", c.usage, botCtx.Event().User)
	c.definition.Handler(botCtx, request, response)
}

func (c *cmd) Interactive(slacker *slacker.Slacker, evt *socketmode.Event, callback *slackgo.InteractionCallback, req *socketmode.Request) {
	if c.definition == nil || c.definition.Interactive == nil {
		return
	}
	c.definition.Interactive(slacker, evt, callback, req)
}

func (c cmd) tokenize() {
	params := c.command.Parameters()
	c.tokens = make([]*commander.Token, len(params))
	for i, param := range params {
		c.tokens[i] = &commander.Token{
			Word: param.Name(),
			Type: 1,
		}
	}
}
