package slack

type Config struct {
	AppToken string `required:"true"`
	BotToken string `required:"true"`
}
