package alloy

import (
	"fmt"
	"io"
	"os"
	"strings"
)

var isDebug = os.Getenv("DEBUG") != ""

type Logger struct {
	out io.Writer
}

func NewLogger() *Logger {
	return &Logger{
		out: os.Stdout,
	}
}

func (l *Logger) Info(msg string) {
	fmt.Fprintf(l.out, "%s\n", msg)
}

func (l *Logger) Success(msg string) {
	fmt.Fprintf(l.out, "%s\n", msg)
}

func (l *Logger) Start(msg string) {
	fmt.Fprintf(l.out, "%s\n", msg)
}

func (l *Logger) Debug(msg string) {
	if isDebug {
		fmt.Fprintf(l.out, "%s\n", msg)
	}
}

func (l *Logger) Error(msg string) {
	fmt.Fprintf(os.Stderr, "%s\n", msg)
}

func (l *Logger) Banner(title string, items []string) {
	fmt.Fprintf(l.out, "\n%s\n", title)
	for _, item := range items {
		fmt.Fprintf(l.out, "%s\n", item)
	}
	fmt.Fprintln(l.out)
}

func IsDebug() bool {
	return isDebug
}

func QuietWriter() io.Writer {
	if isDebug {
		return os.Stdout
	}
	return io.Discard
}

func FormatPath(path string) string {
	cwd, _ := os.Getwd()
	rel := strings.TrimPrefix(path, cwd+"/")
	return rel
}
