package mcpserver

import (
	"context"
	"log/slog"

	"github.com/nevzatcirak/review-mcp/internal/logging"
)

// SDK logging audit (X-8, P4 §0.1), against github.com/modelcontextprotocol/
// go-sdk v1.8.0. The SDK logs through mcp.ServerOptions.Logger (server.go:77)
// only these records: "server run start", "server session connected |
// disconnected | ended" (session id), "resource subscribed | unsubscribed |
// updated" (uri, session id), "keepalive ping failed ..." (shared.go:905,
// 913), "calling <method>: <err>" (shared.go:487, server.go:821), "AddTool:
// invalid tool name" (server.go:317) and "jsonrpc2 internal error"
// (transport.go:234). None logs tool arguments or results on the normal
// path. The one exception is the last: jsonrpc2's internalErrorf formats the
// handler's result with %#v (internal/jsonrpc2/conn.go:700, 723), so a result
// could reach the log if the SDK ever hit that bug path.
//
// sdkLogger is therefore the logger handed to the SDK. It keeps the message
// and the allowlisted attributes and drops every other attribute, and it
// replaces the error text of "jsonrpc2 internal error" with a fixed phrase.
// Our own tool debug lines use deps.Logger directly (counts and redacted
// URLs only) and are not filtered.

// sdkAllowedAttrs are the attribute keys of SDK records that carry only
// identifiers.
var sdkAllowedAttrs = map[string]bool{"session_id": true, "request_id": true, "uri": true, "error": true}

const (
	sdkInternalErrorMessage = "jsonrpc2 internal error"
	sdkWithheld             = "details withheld"
)

// sdkLogger wraps l for use as the SDK's logger; nil stays nil (the SDK then
// discards its logs).
func sdkLogger(l *slog.Logger) *slog.Logger {
	if l == nil {
		return nil
	}
	return slog.New(&sdkFilter{next: l.Handler()})
}

type sdkFilter struct{ next slog.Handler }

func (h *sdkFilter) Enabled(ctx context.Context, lvl slog.Level) bool {
	return h.next.Enabled(ctx, lvl)
}

func (h *sdkFilter) Handle(ctx context.Context, r slog.Record) error {
	out := slog.NewRecord(r.Time, r.Level, logging.RedactText(r.Message), r.PC)
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
