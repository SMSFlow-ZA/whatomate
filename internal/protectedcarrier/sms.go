package protectedcarrier

import (
	"context"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/pkg/channels"
)

// SMSClientID is a deterministic provider UUID scoped to the durable private
// intent. It exposes neither a case reference nor the private intent structure.
func SMSClientID(m delivery.Message) string {
	return uuid.NewSHA1(uuid.NameSpaceURL, []byte("urn:whatomate:protected-sms:"+m.Tenant+"\x00"+m.Account+"\x00"+m.ID)).String()
}

// SMSSender calls the existing compiled provider directly. It never passes a
// protected notice through generic contact/message storage or bot automation.
type SMSSender struct {
	Resolve func(context.Context, delivery.Message) (channels.Provider, channels.Connection, error)
}

func (s SMSSender) Send(ctx context.Context, m delivery.Message) (delivery.Result, string, error) {
	if s.Resolve == nil || m.Channel != "sms" || m.Text != delivery.MinimalSMSNotice || m.ID == "" || len(m.ID) > 512 || !phonePattern.MatchString(m.Sender) || m.Tenant == "" || m.Account == "" {
		return delivery.Unknown, "", errTransport
	}
	p, c, e := s.Resolve(ctx, m)
	if e != nil || p == nil || c.ID != m.Account {
		return delivery.Unknown, "", errTransport
	}
	d := p.Descriptor()
	if d.ID != "smsflow" || d.Channel != "sms" || d.APIVersion != channels.APIVersion {
		return delivery.Unknown, "", errTransport
	}
	result, e := p.Send(ctx, c, channels.Message{ClientMessageID: SMSClientID(m), Destination: m.Sender, Text: delivery.MinimalSMSNotice, CheckOptOuts: true})
	if e != nil || (result.ClientMessageID != "" && result.ClientMessageID != SMSClientID(m)) {
		return delivery.Unknown, "", errTransport
	}
	if result.Status == channels.StatusFailed {
		return delivery.VerifiedFailed, result.ProviderMessageID, nil
	}
	if (result.Status == channels.StatusSubmitted || result.Status == channels.StatusDelivered) && result.ProviderMessageID != "" {
		return delivery.Accepted, result.ProviderMessageID, nil
	}
	return delivery.Unknown, "", errTransport
}
