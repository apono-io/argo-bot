package slack

import (
	"github.com/apono-io/argo-bot/pkg/deploy"
	"github.com/apono-io/argo-bot/pkg/slack/commands"
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

	return &bot{
		client:           client,
		socketmodeClient: socketmodeClient,
		deployConfig:     deployConfig,
	}, nil
}

type bot struct {
	client           *slackgo.Client
	socketmodeClient *socketmode.Client
	deployConfig     deploy.Config
}

func (b *bot) Run() error {
	socketmodeHandler := socketmode.NewSocketmodeHandler(b.socketmodeClient)

	socketmodeHandler.Handle(socketmode.EventTypeConnecting, b.middlewareConnecting)
	socketmodeHandler.Handle(socketmode.EventTypeConnectionError, b.middlewareConnectionError)
	socketmodeHandler.Handle(socketmode.EventTypeConnected, b.middlewareConnected)
	socketmodeHandler.Handle(socketmode.EventTypeHello, b.middlewareNoop)

	d, err := deploy.New(b.deployConfig)
	if err != nil {
		return err
	}
	commands.RegisterCommandHandlers(socketmodeHandler, d)

	socketmodeHandler.HandleDefault(b.middlewareDefault)

	return socketmodeHandler.RunEventLoop()
}

func (b *bot) middlewareConnecting(_ *socketmode.Event, _ *socketmode.Client) {
	log.Info("Connecting to Slack with Socket Mode...")
}

func (b *bot) middlewareConnectionError(_ *socketmode.Event, _ *socketmode.Client) {
	log.Error("Connection failed. Retrying later...")
}

func (b *bot) middlewareConnected(_ *socketmode.Event, _ *socketmode.Client) {
	log.Info("Connected to Slack with Socket Mode.")
}

func (b *bot) middlewareDefault(evt *socketmode.Event, _ *socketmode.Client) {
	log.WithField("event", evt.Data).Warn("Unexpected event type received:", evt.Type)
}

func (b *bot) middlewareNoop(evt *socketmode.Event, _ *socketmode.Client) {
	log.Debug("Ignoring event of type: ", evt.Type)
}
