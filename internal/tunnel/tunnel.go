// Package tunnel publishes the local MCP server on a public address.
//
// The platform can only reach public URLs and a developer machine sits behind
// NAT, so something has to bridge the two. Two backends are offered:
//
//   - cloudflared, which needs no account and works out of the box, but hands
//     out a random address that a mainland-China platform reaches only some of
//     the time (measured: the same address answered in 6s on one launch and was
//     still unreachable 11 minutes later on another);
//   - frp, which needs a server the user runs and gives a stable address in
//     return.
//
// Neither protocol is reimplemented here. Both are supervised as child
// processes, because a tunnel that works is worth more than a tunnel that is
// self-contained.
package tunnel

import (
	"context"
	"time"
)

// Status is what the control panel renders.
type Status struct {
	State     string    `json:"state"` // stopped | starting | up | retrying
	Provider  string    `json:"provider"`
	URL       string    `json:"url"`
	Target    string    `json:"target"`
	Since     time.Time `json:"since"`
	Restarts  int       `json:"restarts"`
	LastError string    `json:"lastError"`
}

// Tunnel is a public address for the local MCP server.
type Tunnel interface {
	// Start launches the tunnel and returns once the process is running; the
	// public address arrives later through the callback.
	Start(ctx context.Context) error
	// Stop terminates it.
	Stop()
	// Status is the current snapshot.
	Status() Status
	// MCPURL is the address the platform should be configured with, or "" while
	// the address is still unknown.
	MCPURL(path string) string
	// SetOnURL registers the callback fired whenever the address becomes known
	// or changes. It runs on the supervisor goroutine, so it must not block.
	SetOnURL(func(string))
}
