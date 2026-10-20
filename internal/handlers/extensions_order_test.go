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
