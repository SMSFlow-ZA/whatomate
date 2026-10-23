//go:build linux

package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestDefinitionCancellationWaitsForExactNamedProbeCleanup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, cleanupStarted, releaseCleanup := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-releaseCleanup:
		default:
			close(releaseCleanup)
		}
	}()
	var name string
	runner := func(ctx context.Context, args ...string) ([]byte, error) {
		switch args[0] {
		case "run":
			for i, v := range args {
				if v == "--name" {
					name = args[i+1]
				}
			}
			if !strings.HasPrefix(name, "er-doc-signature-") {
				t.Error("probe missing owned name")
			}
			close(started)
			<-ctx.Done()
			return nil, ctx.Err()
		case "rm":
			if ctx.Err() != nil || len(args) != 3 || args[1] != "--force" || args[2] != name {
				t.Error("cleanup lost independent context or exact probe")
			}
			close(cleanupStarted)
			<-releaseCleanup
			return nil, nil
		case "ps":
			if args[len(args)-1] != "name=^/"+name+"$" {
				t.Error("cleanup verification not exact")
			}
			return nil, nil
		default:
			return nil, errors.New("unexpected command")
		}
	}
	done := make(chan error, 1)
	go func() { done <- verifyDefinition(ctx, validConfiguration(), "main.cvd", runner) }()
	<-started
	cancel()
	<-cleanupStarted
	select {
	case <-done:
		t.Fatal("returned while owned signature probe cleanup pending")
	default:
	}
	close(releaseCleanup)
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancelled signature probe succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("cleanup did not complete")
	}
}
