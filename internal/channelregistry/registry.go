// Package channelregistry is the host composition root. The channel framework
// itself does not import provider implementations.
package channelregistry

import (
	smsflow "github.com/SMSFlow-ZA/whatomate-smsflow"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"net/http"
)

// Endpoint selection is trusted deployment configuration, never customer input.
func New(smsflowEndpoint string, verifiedReplyRoutes ...string) (*channels.Registry, error) {
	if smsflowEndpoint == "" {
		smsflowEndpoint = smsflow.BaseURL
	}
	provider, err := smsflow.NewWithReplyRoutes(smsflowEndpoint, http.DefaultClient, verifiedReplyRoutes)
	if err != nil {
		return nil, err
	}
	registry := channels.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, err
	}
	return registry, nil
}
