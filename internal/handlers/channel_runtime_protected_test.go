package handlers

import (
	"testing"
	"time"

	"github.com/shridarpatil/whatomate/internal/channelapp"
	"github.com/shridarpatil/whatomate/internal/config"
)

func TestProtectedChannelWorkerStartsNeitherGenericNorBotLanes(t *testing.T) {
	for _, carrierEnabled := range []bool{false, true} {
		name := "carrier_disabled"
		if carrierEnabled {
			name = "carrier_enabled"
		}
		t.Run(name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Extensions.ProtectedMode = true
			cfg.Extensions.CarrierEnabled = carrierEnabled
			// Unavailable DB/provider dependencies must never be used. Dispatch
			// is deliberately not stopped: protected isolation is the boundary.
			app := &App{Config: cfg, Channels: &channelapp.Service{}}
			cancel := app.StartChannelWorker()
			defer cancel()
			finished := make(chan struct{})
			go func() { app.wg.Wait(); close(finished) }()
			select {
			case <-finished:
			case <-time.After(250 * time.Millisecond):
				cancel()
				<-finished
				t.Fatal("protected host started generic channel or SMS bot lanes")
			}
		})
	}
}

func TestOrdinaryChannelWorkerRemainsCancellable(t *testing.T) {
	app := &App{Config: &config.Config{}, Channels: &channelapp.Service{}}
	cancel := app.StartChannelWorker()
	defer cancel()
	finished := make(chan struct{})
	go func() { app.wg.Wait(); close(finished) }()
	select {
	case <-finished:
		t.Fatal("ordinary channel worker unexpectedly disabled")
	case <-time.After(25 * time.Millisecond):
	}
	cancel()
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("ordinary channel worker did not stop after cancellation")
	}
}
