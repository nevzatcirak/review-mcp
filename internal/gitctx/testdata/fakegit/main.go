// Command fakegit stands in for git in the gitctx tests. It reads its
// behaviour from $HOME/fake.json and appends its argv and environment to
// $HOME/calls.jsonl; the runner passes HOME through its allowlist, and each
// test points HOME at its own directory.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type behavior struct {
	Version     string `json:"version"`
	FetchStderr string `json:"fetch_stderr"`
	FetchExit   int    `json:"fetch_exit"`
	SHA         string `json:"sha"`
}

type call struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func main() { os.Exit(run()) }

func run() int {
	home := os.Getenv("HOME")
	if home == "" {
		home = os.Getenv("USERPROFILE")
	}
	if home == "" {
		return 120
	}
	var beh behavior
	if b, err := os.ReadFile(filepath.Join(home, "fake.json")); err == nil {
		_ = json.Unmarshal(b, &beh)
	}
	args := os.Args[1:]
	if line, err := json.Marshal(call{Args: args, Env: os.Environ()}); err == nil {
		f, err := os.OpenFile(filepath.Join(home, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	sub := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--git-dir=") {
			continue
		}
		sub = args[i]
		break
	}
	switch sub {
	case "--version":
		v := beh.Version
		if v == "" {
			v = "git version 2.45.1"
		}
		_, _ = os.Stdout.WriteString(v + "\n")
	case "init":
		_ = os.MkdirAll(args[len(args)-1], 0o700)
	case "fetch":
		_, _ = os.Stderr.WriteString(beh.FetchStderr)
		return beh.FetchExit
	case "rev-parse":
		_, _ = os.Stdout.WriteString(beh.SHA + "\n")
	}
	return 0
}
