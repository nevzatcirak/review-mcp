package mcpserver

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// SDK logging audit (X-8, P4 §0.1, P6 §0 entry criterion 2), against
// github.com/modelcontextprotocol/go-sdk v1.8.0. The SDK logs through
// mcp.ServerOptions.Logger (server.go:77) and, for the HTTP transport,
// mcp.StreamableHTTPOptions.Logger. Its records are:
//
//   - server.go: "server run start", "server connect failed", "server run
//     cancelled", "server session ended [with error]", "server session
//     connected | disconnected" (session id), "server connecting" (:1440),
//     "server connect error" (:1443, error), "initialized before initialize"
//     (:1488), "duplicate initialized notification" (:1492), "session
//     initialized" (:1498), "method removed in the new protocol" (:1972,
//     attr method), "method invalid during initialization" (:1990, attr
//     method), "duplicate initialize request" (:2095), "client log level set"
//     (:2127, attr level), "resource subscribed | unsubscribed | updated"
//     (uri, session id), "calling <method>: <err>" (:821) and "AddTool:
//     invalid tool name" (:317);
//   - shared.go: "keepalive ping failed ..." (:905, :913), "calling
//     <method>: <err>" (:487);
//   - mrtr.go:46: "handler returned both content and inputRequests";
//   - streamable.go: "failed to connect: <err>" (:436, :677, :705) and
//     "Writing close event: <err>" (:1021);
//   - transport.go:234: "jsonrpc2 internal error".
//
// None logs request headers, tool arguments or results on the normal path.
// The one exception is the last: jsonrpc2's internalErrorf formats the
// handler's result with %#v (internal/jsonrpc2/conn.go:700, 723), so a
// result could reach the log if the SDK ever hit that bug path.
//
// sdkLogger is therefore the logger handed to the SDK (both options). It
// keeps the message and the allowlisted attributes and drops every other
// attribute (method, level, ...), and it replaces the error text of
// "jsonrpc2 internal error" with a fixed phrase. Our own tool debug lines use
// deps.Logger directly (counts and redacted URLs only) and are not filtered.
//
// In serve mode the stateless HTTP handler opens and closes a temporary
// session for every request, so the SDK emits "server connecting", "server
// session connected" and "server session disconnected" at info for each one.
// With demoteInfo set, every SDK record at info level is logged at debug
// instead, so the single access line per request stays the only info line
// (P6 §1.4). Warnings and errors keep their level.

// sdkAllowedAttrs are the attribute keys of SDK records that carry only
// identifiers.
var sdkAllowedAttrs = map[string]bool{"session_id": true, "request_id": true, "uri": true, "error": true}

const (
	sdkInternalErrorMessage = "jsonrpc2 internal error"
	sdkWithheld             = "details withheld"
)

// sdkLogger wraps l for use as the SDK's logger; nil stays nil (the SDK then
// discards its logs). demoteInfo logs the SDK's info records at debug.
func sdkLogger(l *slog.Logger, demoteInfo bool) *slog.Logger {
	if l == nil {
		return nil
	}
	return slog.New(&sdkFilter{next: l.Handler(), demoteInfo: demoteInfo})
}

type sdkFilter struct {
	next       slog.Handler
	demoteInfo bool
}

// level maps a record level to the level it is logged at.
func (h *sdkFilter) level(lvl slog.Level) slog.Level {
	if h.demoteInfo && lvl >= slog.LevelInfo && lvl < slog.LevelWarn {
		return slog.LevelDebug
	}
	return lvl
}

func (h *sdkFilter) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.next.Enabled(ctx, h.level(lvl))
}

func (h *sdkFilter) Handle(ctx context.Context, r slog.Record) error {
	lvl := h.level(r.Level)
	if !h.next.Enabled(ctx, lvl) {
		return nil
	}
	out := slog.NewRecord(r.Time, lvl, logging.RedactText(r.Message), r.PC)
	r.Attrs(func(a slog.Attr) bool {
		if !sdkAllowedAttrs[a.Key] {
			return true
		}
		if a.Key == "error" {
			if r.Message == sdkInternalErrorMessage {
				a = slog.String("error", sdkWithheld)
			} else {
				a = slog.String("error", logging.RedactText(a.Value.String()))
			}
		}
		out.AddAttrs(a)
		return true
	})
	return h.next.Handle(ctx, out)
}

// WithAttrs and WithGroup apply the same rule to attributes bound ahead of
// time: nothing is bound by the SDK, so they are dropped.
func (h *sdkFilter) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *sdkFilter) WithGroup(string) slog.Handler      { return h }
