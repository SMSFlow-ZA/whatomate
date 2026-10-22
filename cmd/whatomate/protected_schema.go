package main

import (
	"github.com/shridarpatil/whatomate/internal/config"
	"github.com/shridarpatil/whatomate/internal/protectedschema"
	"strings"
)

func preflightExternalSchema(c *config.Config, migrate bool, source string) error {
	if c == nil {
		return protectedschema.ErrDenied
	}
	if !c.Database.SchemaManagedExternally {
		return nil
	}
	if migrate || protectedschema.Validate(c) != nil || source == "" || strings.Split(protectedschema.ReleaseIdentity, ":")[0] != source {
		return protectedschema.ErrDenied
	}
	return nil
}

func preflightStandaloneWorker(c *config.Config) error {
	if c == nil || c.Database.SchemaManagedExternally {
		return protectedschema.ErrDenied
	}
	return preflightServerWorkers(c, 1)
}
