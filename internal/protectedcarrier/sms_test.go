package protectedcarrier

import (
	"context"
	"errors"
	"github.com/SMSFlow-ZA/whatomate-addons/delivery"
	"github.com/google/uuid"
	"github.com/shridarpatil/whatomate/pkg/channels"
	"testing"
)

type smsTestProvider struct {
	calls    int
	out      channels.Submission
	err      error
	received channels.Message
}

func (p *smsTestProvider) Descriptor() channels.Descriptor {
	return channels.Descriptor{ID: "smsflow", Version: "test", APIVersion: 1, Channel: "sms"}
}
func (p *smsTestProvider) Validate(context.Context, channels.Connection) (channels.ConnectionInfo, error) {
	return channels.ConnectionInfo{}, nil
}
func (p *smsTestProvider) Send(_ context.Context, _ channels.Connection, m channels.Message) (channels.Submission, error) {
	p.calls++
	p.received = m
	return p.out, p.err
}
func (p *smsTestProvider) Reconcile(context.Context, channels.Connection, string) (channels.Submission, error) {
	return channels.Submission{}, nil
}
func (p *smsTestProvider) Poll(context.Context, channels.Connection, string) (channels.EventPage, error) {
	return channels.EventPage{}, nil
}
func smsSender(p *smsTestProvider) SMSSender {
	return SMSSender{Resolve: func(context.Context, delivery.Message) (channels.Provider, channels.Connection, error) {
		return p, channels.Connection{ID: "sms-account"}, nil
	}}
}
func smsMessage() delivery.Message {
	m := validMessage()
	m.Channel = "sms"
	m.Account = "sms-account"
	m.Text = delivery.MinimalSMSNotice
	return m
}
func TestSMSUsesOnlyMinimalNoticeAndStableIntent(t *testing.T) {
	p := &smsTestProvider{out: channels.Submission{Status: channels.StatusSubmitted, ProviderMessageID: "sms-provider"}}
	result, id, e := smsSender(p).Send(context.Background(), smsMessage())
	if e != nil || result != delivery.Accepted || id != "sms-provider" || p.calls != 1 {
		t.Fatal("SMS acceptance not observed")
	}
	if p.received.Text != delivery.MinimalSMSNotice || p.received.ClientMessageID == "opaque-intent" || !p.received.CheckOptOuts {
		t.Fatal("unsafe SMS payload")
	}
	m := smsMessage()
	m.Text = "Synthetic confidential case narrative"
	_, _, _ = smsSender(p).Send(context.Background(), m)
	if p.calls != 1 {
		t.Fatal("case content leaked to SMS")
	}
}
func TestSMSUnknownDoesNotRetry(t *testing.T) {
	p := &smsTestProvider{err: errors.New("synthetic network timeout")}
	result, _, _ := smsSender(p).Send(context.Background(), smsMessage())
	if result != delivery.Unknown || p.calls != 1 {
		t.Fatal("uncertain SMS must remain one-shot unknown")
	}
}

func TestSMSProviderCorrelationIsStableUUIDAndTenantScoped(t *testing.T) {
	p := &smsTestProvider{out: channels.Submission{Status: channels.StatusSubmitted, ProviderMessageID: "provider"}}
	sender := smsSender(p)
	m := smsMessage()
	_, _, _ = sender.Send(context.Background(), m)
	first := p.received.ClientMessageID
	if _, e := uuid.Parse(first); e != nil {
		t.Fatal("pinned provider requires a UUID correlation", e)
	}
	_, _, _ = sender.Send(context.Background(), m)
	if p.received.ClientMessageID != first {
		t.Fatal("correlation changed on same durable intent")
	}
	m.Tenant = "another-org"
	_, _, _ = sender.Send(context.Background(), m)
	if p.received.ClientMessageID == first {
		t.Fatal("correlation collided across tenants")
	}
}
