// Package channelregistry is the host composition root. The channel framework
// itself does not import provider implementations.
package channelregistry

import (
	smsflow "github.com/SMSFlow-ZA/whatomate-smsflow"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"net/http"
)

// Endpoint selection is trusted deployment configuration, never customer input.
func New(smsflowEndpoint string) (*channels.Registry, error) {
	provider := smsflow.New()
	if smsflowEndpoint != "" {
		var err error
		provider, err = smsflow.NewWithClient(smsflowEndpoint, http.DefaultClient)
		if err != nil {
			return nil, err
		}
	}
	registry := channels.NewRegistry()
	if err := registry.Register(provider); err != nil {
		return nil, err
	}
	return registry, nil
}
