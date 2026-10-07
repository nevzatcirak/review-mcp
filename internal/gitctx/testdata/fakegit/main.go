// Command fakegit stands in for git in the gitctx tests. It reads its
// behaviour from fake.json next to its own binary and appends its argv and
// environment to calls.jsonl there: the runner gives git an isolated HOME,
// so the test's state lives beside the binary, which each test installs
// under a directory of its own.
//
// It keeps remote.origin.url (from "git config remote.origin.url <url>")
// in remote-url and answers "ls-remote --get-url origin" with it, rewritten
// by any url.<base>.insteadOf given through GIT_CONFIG_* (longest match), as
// git does.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type behavior struct {
	Version     string `json:"version"`
	FetchStderr string `json:"fetch_stderr"`
	FetchExit   int    `json:"fetch_exit"`
	SHA         string `json:"sha"`
	// Search (WP-11b): what "grep" prints and exits with, its stderr, how
	// long it takes, and what "rev-list" prints.
	GrepOut     string `json:"grep_out"`
	GrepExit    int    `json:"grep_exit"`
	GrepStderr  string `json:"grep_stderr"`
	GrepSleepMS int    `json:"grep_sleep_ms"`
	RevListOut  string `json:"revlist_out"`
}

type call struct {
	Args []string `json:"args"`
	Env  []string `json:"env"`
}

func main() { os.Exit(run()) }

func run() int {
	dir := filepath.Dir(os.Args[0])
	if !filepath.IsAbs(dir) {
		return 120
	}
	var beh behavior
	if b, err := os.ReadFile(filepath.Join(dir, "fake.json")); err == nil {
		_ = json.Unmarshal(b, &beh)
	}
	args := os.Args[1:]
	if line, err := json.Marshal(call{Args: args, Env: os.Environ()}); err == nil {
		f, err := os.OpenFile(filepath.Join(dir, "calls.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err == nil {
			_, _ = f.Write(append(line, '\n'))
			_ = f.Close()
		}
	}
	sub, rest := "", []string(nil)
	for i := 0; i < len(args); i++ {
		if args[i] == "-c" {
			i++
			continue
		}
		if strings.HasPrefix(args[i], "--git-dir=") {
			continue
		}
		sub, rest = args[i], args[i+1:]
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
	case "config":
		if len(rest) == 2 && rest[0] == "remote.origin.url" {
			_ = os.WriteFile(filepath.Join(dir, "remote-url"), []byte(rest[1]), 0o600)
		}
	case "ls-remote":
		if len(rest) == 2 && rest[0] == "--get-url" {
			u := rest[1]
			if u == "origin" {
				b, _ := os.ReadFile(filepath.Join(dir, "remote-url"))
				u = string(b)
			}
			_, _ = os.Stdout.WriteString(insteadOf(u) + "\n")
		}
	case "fetch":
		_, _ = os.Stderr.WriteString(beh.FetchStderr)
		return beh.FetchExit
	case "grep":
		if beh.GrepSleepMS > 0 {
			time.Sleep(time.Duration(beh.GrepSleepMS) * time.Millisecond)
		}
		_, _ = os.Stderr.WriteString(beh.GrepStderr)
		_, _ = os.Stdout.WriteString(beh.GrepOut)
		return beh.GrepExit
	case "rev-list":
		_, _ = os.Stdout.WriteString(beh.RevListOut)
	case "rev-parse":
		_, _ = os.Stdout.WriteString(beh.SHA + "\n")
	}
	return 0
}

// insteadOf applies the url.<base>.insteadOf entries of the environment's
// GIT_CONFIG_* to u.
func insteadOf(u string) string {
	n, _ := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT"))
	best, bestLen := u, 0
	for i := 0; i < n; i++ {
		k := os.Getenv("GIT_CONFIG_KEY_" + strconv.Itoa(i))
		v := os.Getenv("GIT_CONFIG_VALUE_" + strconv.Itoa(i))
		if !strings.HasPrefix(k, "url.") || !strings.HasSuffix(strings.ToLower(k), ".insteadof") {
			continue
		}
		base := k[len("url.") : len(k)-len(".insteadOf")]
		if v != "" && strings.HasPrefix(u, v) && len(v) > bestLen {
			best, bestLen = base+u[len(v):], len(v)
		}
	}
	return best
}
