package rulesets

import (
	"errors"
	"io"
	"log/slog"
)

var errRateLimited = errors.New("status 403")

type nopCounter struct{}

func (nopCounter) Count(string) (int, error) { return 0, nil }

var noCounter = nopCounter{}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
