package logging

import (
	log "github.com/sirupsen/logrus"
)

func NewLogTypeFormatter(formatter log.Formatter, logType string) log.Formatter {
	return &logTypeFormatter{
		formatter: formatter,
		logType:   logType,
	}
}

type logTypeFormatter struct {
	logType   string
	formatter log.Formatter
}

func (f *logTypeFormatter) Format(entry *log.Entry) ([]byte, error) {
	entry.Data["type"] = f.logType
	return f.formatter.Format(entry)
}
