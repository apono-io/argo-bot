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
	"strings"
)

type Bot interface {
	Run() error
}

func New(config Config, deployConfig deploy.Config) (Bot, error) {
	slackerBot := slacker.NewClient(config.BotToken, config.AppToken,
		slacker.WithDebug(false),
	)

	return &bot{
		deployConfig: deployConfig,
		slackerBot:   slackerBot,
	}, nil
}

type bot struct {
	deployConfig      deploy.Config
	slackerBot        *slacker.Slacker
	botName           string
	botUserId         string
	botMentionPattern string
}

func (b *bot) Run() error {
	authInfo, err := b.slackerBot.Client().AuthTest()
	if err != nil {
		return err
	}

	b.botName = authInfo.User
	b.botUserId = authInfo.UserID
	b.botMentionPattern = fmt.Sprintf("<@%s>", b.botUserId)

	b.slackerBot.CustomCommand(b.constructCommand)
	b.slackerBot.CustomBotContext(b.constructBotContext)

	d, err := deploy.New(b.deployConfig)
	if err != nil {
		return err
	}

	commands.RegisterCommandHandlers(b.slackerBot, d)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	return b.slackerBot.Listen(ctx)
}

func (b *bot) constructCommand(usage string, definition *slacker.CommandDefinition) slacker.BotCommand {
	c := &cmd{
		usage:      usage,
		definition: definition,
		command:    allot.New(fmt.Sprintf("%s %s", b.botName, usage)),
	}
	c.tokenize()
	return c
}

func (b *bot) constructBotContext(ctx context.Context, client *slackgo.Client, socketmode *socketmode.Client, evt *slacker.MessageEvent) slacker.BotContext {
	if evt.Channel[0] == 'D' && strings.Index(evt.Text, b.botName) != 0 {
		evt.Text = fmt.Sprintf("%s %s", b.botName, strings.TrimSpace(evt.Text))
	} else if strings.Index(evt.Text, b.botMentionPattern) != 0 {
		evt.Text = strings.Replace(evt.Text, b.botMentionPattern, b.botName, 1)
	}

	return slacker.NewBotContext(ctx, client, socketmode, evt)
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

func (c *cmd) tokenize() {
	params := c.command.Parameters()
	paramIdx := 0

	strTokens := strings.Split(strings.TrimSpace(c.command.Text()), " ")
	c.tokens = make([]*commander.Token, len(strTokens))
	for i, token := range strTokens {
		if strings.Index(token, "<") == 0 {
			c.tokens[i] = &commander.Token{
				Word: params[paramIdx].Name(),
				Type: "WORD_PARAMETER",
			}
			paramIdx++
		} else {
			c.tokens[i] = &commander.Token{
				Word: token,
				Type: "NOT_PARAMETER",
			}
		}
	}
}
