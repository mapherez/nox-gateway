// Package logging exposes structured lifecycle logs, not request/secret dumps.
package logging

import (
	"io"
	"log"
	"log/slog"
)

func New(out io.Writer, level slog.Level) *slog.Logger {
	return slog.New(slog.NewJSONHandler(out, &slog.HandlerOptions{Level: level}))
}

// HTTPErrorLog discards raw transport diagnostics. net/http can include request
// bytes or panic values in these messages; they must not enter ordinary logs.
func HTTPErrorLog(logger *slog.Logger) *log.Logger {
	return log.New(httpErrorWriter{logger}, "", 0)
}

type httpErrorWriter struct{ logger *slog.Logger }

func (w httpErrorWriter) Write(p []byte) (int, error) {
	w.logger.Debug("http_connection_error")
	return len(p), nil
}
