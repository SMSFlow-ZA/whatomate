package models

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInboxRoutingPoliciesKeepCapacityAndRollingRateIndependent(t *testing.T) {
	p := InboxRoutingPolicy{Name: "Support routing", Strategy: InboxRoutingInherit}
	require.NoError(t, p.Validate(), "an unapplied template may preserve team strategy and all existing capacity")
	zero := 0
	p.MaxActive = &zero
	require.NoError(t, p.Validate(), "unlimited active capacity does not turn on a rate limit")
	p.MaxAssignments, p.WindowSeconds = 10, 3600
	require.NoError(t, p.Validate())
	for _, change := range []func(*InboxRoutingPolicy){
		func(p *InboxRoutingPolicy) { p.Name = " " },
		func(p *InboxRoutingPolicy) { p.Name = strings.Repeat("é", 101) },
		func(p *InboxRoutingPolicy) { p.Description = strings.Repeat("é", 1001) },
		func(p *InboxRoutingPolicy) { p.Strategy = "random" },
		func(p *InboxRoutingPolicy) { p.MaxAssignments = 0 },
		func(p *InboxRoutingPolicy) { p.MaxAssignments = 10001 },
		func(p *InboxRoutingPolicy) { p.WindowSeconds = 59 },
		func(p *InboxRoutingPolicy) { p.WindowSeconds = 86401 },
		func(p *InboxRoutingPolicy) { negative := -1; p.MaxActive = &negative },
	} {
		invalid := p
		change(&invalid)
		require.Error(t, invalid.Validate())
	}
	for _, strategy := range []AssignmentStrategy{InboxRoutingInherit, AssignmentStrategyManual, AssignmentStrategyRoundRobin, AssignmentStrategyLoadBalanced} {
		p.Strategy = strategy
		require.NoError(t, p.Validate())
	}
	encoded, err := json.Marshal(InboxRoutingConfiguration{ApplyReceipt: "private-confirmation", PolicyRevision: 1})
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "private-confirmation")
	receipt, err := json.Marshal(InboxAssignmentReceipt{ID: 17})
	require.NoError(t, err)
	require.JSONEq(t, `{}`, string(receipt), "assignment ledger rows are never a public DTO")
}
