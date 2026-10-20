package channelapp

import (
	"context"
	"github.com/shridarpatil/whatomate/pkg/channels"
)

type testProtectedProvider struct{}

func (*testProtectedProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "test-private", Version: "1", APIVersion: 1, Channel: "sms"}
}
func (*testProtectedProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{}, nil
}
func (*testProtectedProvider) Send(context.Context, channels.Connection, channels.Message) (channels.Submission, error) {
	return channels.Submission{}, nil
}
func (*testProtectedProvider) Reconcile(context.Context, channels.Connection, string) (channels.Submission, error) {
	return channels.Submission{}, nil
}
func (*testProtectedProvider) Poll(context.Context, channels.Connection, string) (channels.EventPage, error) {
	return channels.EventPage{}, nil
}
