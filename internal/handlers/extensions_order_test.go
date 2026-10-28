package handlers

import "testing"

func TestModuleMigrationsFollowDependenciesAndValidateBeforeDDL(t *testing.T) {
	order, e := orderedExtensionModules([]string{"governance", "reports", "conversation", "ai", "knowledge", "cases", "directory"})
	if e != nil {
		t.Fatal(e)
	}
	index := map[string]int{}
	for i, id := range order {
		index[id] = i
	}
	for _, edge := range [][2]string{{"directory", "cases"}, {"cases", "ai"}, {"knowledge", "ai"}, {"ai", "conversation"}, {"conversation", "reports"}, {"reports", "governance"}} {
		if index[edge[0]] >= index[edge[1]] {
			t.Fatal(order, edge)
		}
	}
	for _, ids := range [][]string{{"cases"}, {"directory", "directory"}, {"knowledge", "unknown"}} {
		if _, e := orderedExtensionModules(ids); e == nil {
			t.Fatal("unsafe composition", ids)
		}
	}
}

func TestNotificationModuleRequiresConversationAndPrecedesGovernance(t *testing.T) {
	ids := []string{"governance", "notifications", "reports", "conversation", "ai", "knowledge", "cases", "directory"}
	order, err := orderedExtensionModules(ids)
	if err != nil {
		t.Fatalf("compiled notification composition rejected: %v", err)
	}
	positions := map[string]int{}
	for i, id := range order {
		positions[id] = i
	}
	if positions["conversation"] >= positions["notifications"] || positions["notifications"] >= positions["governance"] {
		t.Fatalf("notification owner migration order is unsafe: %v", order)
	}
	if _, err := orderedExtensionModules([]string{"notifications"}); err == nil {
		t.Fatal("notification module accepted without protected dependencies")
	}
	if _, err := orderedExtensionModules([]string{"directory", "cases", "knowledge", "ai", "conversation", "reports", "governance"}); err != nil {
		t.Fatalf("omitting notifications must preserve existing composition: %v", err)
	}
}
