package provider

import (
	"context"
	"net/url"
)

type ctxT = context.Context

func urlUnescape(s string) (string, error) { return url.PathUnescape(s) }
