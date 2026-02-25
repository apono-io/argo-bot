package slack

type Config struct {
	AppToken string `required:"true"`
	BotToken string `required:"true"`
	Debug    bool   // Set automatically based on log level
}
