// Package pools keeps a tunnel open to every pool this machine serves, and
// keeps that set in step with the server: a machine can be added to another
// organization's pool, or dropped from one, without being touched.
package pools

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"time"

	"github.com/thecomputerplumbers/hiveworker/daemon/internal/api"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/jobs"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/protocol"
	"github.com/thecomputerplumbers/hiveworker/daemon/internal/tunnel"
)

type Options struct {
	Client        *api.Client
	Token         string
	DaemonVersion string
	// Available says whether the machine's owner would mind a job right now.
	Available func() bool
	// Limits is what the owner lets the machine lend at once. It may change
	// while the daemon runs. Nil means one job of any size.
	Limits func() jobs.Limits
	// Runner runs jobs. Nil means the machine connects and reports presence
	// but declines every job.
	Runner jobs.Runner
	// RefreshEvery is how often the server is asked which pools to serve.
	// Defaults to a minute.
	RefreshEvery time.Duration
	Log          *slog.Logger
	// Tunnel overrides are for tests.
	PresenceEvery time.Duration
	PingEvery     time.Duration
}

type ended struct {
	pool string
	err  error
}

// Run serves every pool the machine is authorized for until ctx is cancelled,
// the machine is retired, or a tunnel reports something only a person can fix.
func Run(ctx context.Context, options Options) error {
	if options.RefreshEvery == 0 {
		options.RefreshEvery = time.Minute
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	slot := jobs.New(options.Runner, options.Log)
	slot.Limit(options.Limits)
	presence := func() protocol.Presence {
		// Online means willing to take another job, whether or not some
		// are running. A machine whose owner is holding it back stays busy
		// until the jobs it has are done, and is paused after that.
		switch available := options.Available(); {
		case slot.Full(), slot.Busy() && !available:
			return protocol.Busy
		case available:
			return protocol.Online
		default:
			return protocol.Paused
		}
	}

	running := map[string]context.CancelFunc{}
	finished := make(chan ended)
	defer func() {
		// Let each tunnel say goodbye before the process exits.
		cancel()
		for range running {
			<-finished
		}
		// And let a job that was running have its VM destroyed. Returning
		// sooner would leave its disk, and its credential, on the machine.
		slot.Wait()
	}()

	refresh := time.NewTimer(0)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil

		case done := <-finished:
			delete(running, done.pool)
			switch {
			case errors.Is(done.err, tunnel.ErrRevoked):
				// Dropped from one pool. Ask the server what is left.
				options.Log.Info("no longer serving a pool", "pool", done.pool)
				refresh.Reset(0)
			case done.err != nil:
				return done.err
			}

		case <-refresh.C:
			refresh.Reset(options.RefreshEvery)
			assignment, err := options.Client.Me(ctx, options.Token)
			if errors.Is(err, api.ErrUnknownMachine) {
				return err
			}
			if err != nil {
				// The server being unreachable changes nothing: tunnels
				// already open stay open.
				options.Log.Warn("could not refresh the pool list", "error", err)
				continue
			}
			wanted := map[string]api.Pool{}
			for _, pool := range assignment.Pools {
				wanted[pool.ID] = pool
			}
			for id, stop := range running {
				if _, ok := wanted[id]; !ok {
					// The server no longer lists it. Say so here too: this
					// can win the race with the coordinator closing the
					// tunnel, and the owner should see why either way.
					options.Log.Info("no longer serving a pool", "pool", id)
					stop()
				}
			}
			if len(wanted) == 0 && len(running) == 0 {
				options.Log.Warn("this machine serves no pools; add it to one from the dashboard")
			}
			for id, pool := range wanted {
				if _, ok := running[id]; ok {
					continue
				}
				address, err := tunnelURL(assignment.TunnelURL, id)
				if err != nil {
					return err
				}
				poolCtx, stop := context.WithCancel(ctx)
				running[id] = stop
				options.Log.Info("serving a pool", "pool", pool.Name)
				go func() {
					err := tunnel.Run(poolCtx, tunnel.Options{
						URL:           address,
						PoolID:        id,
						Pool:          pool.Name,
						Token:         options.Token,
						DaemonVersion: options.DaemonVersion,
						Presence:      presence,
						Limits:        options.Limits,
						PresenceEvery: options.PresenceEvery,
						PingEvery:     options.PingEvery,
						Jobs:          slot,
						JobContext:    ctx,
						Log:           options.Log,
					})
					stop()
					finished <- ended{pool: id, err: err}
				}()
			}
		}
	}
}

func tunnelURL(base, pool string) (string, error) {
	address, err := url.Parse(base)
	if err != nil {
		return "", err
	}
	query := address.Query()
	query.Set("pool", pool)
	address.RawQuery = query.Encode()
	return address.String(), nil
}
