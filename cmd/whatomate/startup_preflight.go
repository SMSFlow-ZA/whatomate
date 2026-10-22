package main

import (
	"fmt"

	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/worker"
)

// Validate process options before connecting, migrating or irreversibly
// activating a protected database. Protected work uses the server's dedicated
// extension worker, never the generic campaign consumer.
func preflightServerWorkers(c *config.Config, count int) error {
	if c == nil || count < 0 {
		return fmt.Errorf("invalid server worker configuration")
	}
	if count == 0 {
		return nil
	}
	if err := worker.ValidateConfiguration(c); err != nil {
		return fmt.Errorf("server worker configuration requires -workers 0: %w", err)
	}
	return nil
}
