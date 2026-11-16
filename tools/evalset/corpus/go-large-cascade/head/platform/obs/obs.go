// Package obs writes the shop's logs as JSON lines, one per event, with the
// time, the level, the service name and the message, so that the log
// collector can index them. It replaces the standard log package in the
// services; the call sites keep their Printf-style messages.
package obs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sync"
	"time"
)

var (
	mu      sync.Mutex
	out     io.Writer = os.Stderr
	service           = "unknown"
	now               = time.Now
	exit              = os.Exit
)

type line struct {
	Time    string `json:"time"`
	Level   string `json:"level"`
	Service string `json:"service"`
	Msg     string `json:"msg"`
}

// SetService names the service in every following line.
func SetService(name string) {
	mu.Lock()
	defer mu.Unlock()
	service = name
}

// Printf writes an info line; the message is formatted as by fmt.Sprintf.
func Printf(format string, args ...any) {
	write("info", fmt.Sprintf(format, args...))
}

// Fatal writes an error line, formatted as by fmt.Sprint, and exits with
// status 1.
func Fatal(args ...any) {
	write("error", fmt.Sprint(args...))
	exit(1)
}

func write(level, msg string) {
	mu.Lock()
	defer mu.Unlock()
	b, err := json.Marshal(line{Time: now().UTC().Format(time.RFC3339Nano), Level: level, Service: service, Msg: msg})
	if err != nil {
		return
	}
	_, _ = out.Write(append(b, '\n'))
}
