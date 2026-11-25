package inboxlive

import (
	"context"
	"time"

	"github.com/google/uuid"
)

const UpdateBuffer = 128

type Reason string

const (
	Starting      Reason = "starting"
	Connected     Reason = "connected"
	ConnectFailed Reason = "connect_failed"
	ListenFailed  Reason = "listen_failed"
	ReadFailed    Reason = "read_failed"
	InvalidEvent  Reason = "invalid_event"
	Overflow      Reason = "event_overflow"
	Stopped       Reason = "stopped"
)

// Source owns one authenticated database notification connection. Wait and
// Close are called serially by the listener; both must honour their context.
type Source interface {
	Wait(context.Context) ([]byte, error)
	Close(context.Context) error
}

type Connector func(context.Context) (Source, error)

// Update is internal transport state. Public frames must be independently
// scoped/authorised and use opaque resource keys rather than these DB events.
type Update struct {
	Epoch     uuid.UUID `json:"-"`
	Available bool      `json:"-"`
	Reset     bool      `json:"-"`
	Reason    Reason    `json:"-"`
	Event     *Event    `json:"-"`
}

type ListenerOptions struct {
	RetryMin time.Duration
	RetryMax time.Duration
}

// Listen starts one bounded goroutine. Lost/invalid hints and reconnects force a
// new epoch and authoritative resynchronisation. Neither failures nor private
// payloads are logged here; callers can record the safe Reason categories.
func Listen(ctx context.Context, connect Connector, options ListenerOptions) <-chan Update {
	updates := make(chan Update, UpdateBuffer)
	minimum, maximum := options.RetryMin, options.RetryMax
	if minimum <= 0 {
		minimum = 500 * time.Millisecond
	}
	if minimum < time.Millisecond {
		minimum = time.Millisecond
	}
	if maximum <= 0 || maximum > 15*time.Second {
		maximum = 15 * time.Second
	}
	if maximum < time.Millisecond {
		maximum = time.Millisecond
	}
	if minimum > maximum {
		minimum = maximum
	}
	go func() {
		defer close(updates)
		epoch := uuid.New()
		available := false
		reset := func(reason Reason, connected bool) {
			// Single producer: discard obsolete hints so a reset cannot be stuck
			// behind a full queue or dropped. Every discarded hint is reconciled.
			for i := 0; i < UpdateBuffer; i++ {
				select {
				case <-updates:
				default:
					i = UpdateBuffer
				}
			}
			epoch, available = uuid.New(), connected
			updates <- Update{Epoch: epoch, Available: available, Reset: true, Reason: reason}
		}
		reset(Starting, false)
		retry := minimum
		for ctx.Err() == nil {
			connectionCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			source, err := connect(connectionCtx)
			cancel()
			if err != nil || source == nil {
				reason := ConnectFailed
				if err == errListenFailed {
					reason = ListenFailed
				}
				reset(reason, false)
			} else {
				reset(Connected, true)
				for ctx.Err() == nil {
					payload, readErr := source.Wait(ctx)
					if readErr != nil {
						break
					}
					retry = minimum
					event, decodeErr := Decode(payload)
					if decodeErr != nil {
						reset(InvalidEvent, true)
						continue
					}
					select {
					case updates <- Update{Epoch: epoch, Available: available, Event: &event}:
					default:
						reset(Overflow, true)
					}
				}
				closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
				_ = source.Close(closeCtx)
				closeCancel()
				if ctx.Err() == nil {
					reset(ReadFailed, false)
				}
			}
			if ctx.Err() != nil {
				break
			}
			timer := time.NewTimer(retry)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			if retry < maximum/2 {
				retry *= 2
			} else {
				retry = maximum
			}
		}
		reset(Stopped, false)
	}()
	return updates
}
