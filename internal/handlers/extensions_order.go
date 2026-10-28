package handlers

import "fmt"

// Dependencies determine migrations; CSV order is not a migration contract.
func orderedExtensionModules(ids []string) ([]string, error) {
	deps := map[string][]string{"directory": {}, "cases": {"directory"}, "knowledge": {}, "ai": {"cases", "knowledge"}, "conversation": {"directory", "cases", "knowledge", "ai"}, "notifications": {"cases", "conversation"}, "reports": {"cases", "conversation"}, "governance": {"directory", "cases", "knowledge", "ai", "conversation", "reports"}}
	enabled := map[string]bool{}
	for _, id := range ids {
		if _, ok := deps[id]; !ok {
			return nil, fmt.Errorf("module not compiled: %s", id)
		}
		if enabled[id] {
			return nil, fmt.Errorf("duplicate compiled module")
		}
		enabled[id] = true
	}
	if enabled["notifications"] {
		deps["governance"] = append(deps["governance"], "notifications")
	}
	var result []string
	visited := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if visited[id] {
			return nil
		}
		for _, dep := range deps[id] {
			if !enabled[dep] {
				return fmt.Errorf("module %s requires %s", id, dep)
			}
			if e := visit(dep); e != nil {
				return e
			}
		}
		visited[id] = true
		result = append(result, id)
		return nil
	}
	for _, id := range ids {
		if e := visit(id); e != nil {
			return nil, e
		}
	}
	return result, nil
}
