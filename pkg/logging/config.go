package logging

type Config struct {
	LogType               string `default:"argo-bot" required:"true"`
	LogLevel              string `default:"info"`
	LogzioListenerAddress string
	LogzioLoggingToken    string
}
