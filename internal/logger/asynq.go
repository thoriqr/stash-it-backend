package logger

import (
	"fmt"

	"github.com/hibiken/asynq"
	"go.uber.org/zap"
)

// Asynq returns a zap logger behind Asynq's own logging interface.
//
// Asynq logs through a variadic any interface and, by default, writes
// fmt-style lines to stderr. Left alone that produces a stream of unstructured
// output interleaved with this project's structured fields, from the same
// process, which makes the worker's logs harder to read than they need to be.
// The adapter costs nothing and keeps one log format per binary.
//
// The arguments are rendered into the message rather than attached as fields.
// Asynq passes a format string and its arguments separately, so anything that can
// be turned into a field pair is already a pair and anything that cannot is not
// worth inventing a field name for.
func Asynq(log *zap.Logger) asynq.Logger {
	return asynqLogger{log: log}
}

type asynqLogger struct {
	log *zap.Logger
}

func (l asynqLogger) Debug(args ...any) {
	l.log.Debug(asynqMessage(args...))
}

func (l asynqLogger) Info(args ...any) {
	l.log.Info(asynqMessage(args...))
}

func (l asynqLogger) Warn(args ...any) {
	l.log.Warn(asynqMessage(args...))
}

func (l asynqLogger) Error(args ...any) {
	l.log.Error(asynqMessage(args...))
}

// Fatal maps Asynq's fatal level onto zap's error level.
//
// Asynq documents Fatal as exiting the process, and nothing in this project asks
// it to: the process exits when the server stops, or when it fails to start, both
// of which main() already handles. A logger that called os.Exit from inside a log
// call would make that untestable and would bypass the deferred pool and Redis
// closes, so the level is downgraded instead of the behaviour being reproduced.
func (l asynqLogger) Fatal(args ...any) {
	l.log.Error(asynqMessage(args...))
}

// asynqMessage renders Asynq's argument list as a single message.
//
// When a caller supplies a format string with arguments, fmt.Sprintf applies the
// format. When it supplies a lone message with no arguments, the message is used
// as it is, which avoids a round trip through the parser and keeps a message
// containing a percent sign intact.
func asynqMessage(args ...any) string {
	if len(args) == 0 {
		return ""
	}

	format, ok := args[0].(string)
	if !ok || len(args) == 1 {
		return fmt.Sprint(args...)
	}

	return fmt.Sprintf(format, args[1:]...)
}
