package main

import (
	"context"
	"encoding/json"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/app"
)

// The control methods. Each console command calls one of them.
const methodStatus = "status"

// statusResult is the result of the status method.
type statusResult struct {
	Version   string    `json:"version"`
	PID       int       `json:"pid"`
	StartedAt time.Time `json:"started_at"`
	// Uptime is in whole seconds.
	Uptime int64 `json:"uptime"`
}

// registerAPI registers the handler of every control method on srv.
func registerAPI(srv *control.Server, health *app.Health) {
	srv.Handle(methodStatus, func(context.Context, json.RawMessage, control.Stream) (any, error) {
		s := health.Status()
		return statusResult{
			Version:   s.Version,
			PID:       s.PID,
			StartedAt: s.StartedAt,
			Uptime:    int64(s.Uptime / time.Second),
		}, nil
	})
}
