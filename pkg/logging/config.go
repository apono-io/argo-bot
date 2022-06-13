package logging

type Config struct {
	LogType               string `default:"argo-bot" required:"true"`
	LogzioListenerAddress string
	LogzioLoggingToken    string
}
