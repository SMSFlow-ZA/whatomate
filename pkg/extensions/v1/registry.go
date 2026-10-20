// Package extensions defines the compiled-in, versioned add-on boundary.
// It deliberately exports no host database handles or internal model types.
package extensions

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"
)

const APIVersion = 1

var ErrDenied = errors.New("extension access denied")
var ErrConflict = errors.New("extension version conflict")
var ErrUnavailable = errors.New("extension unavailable")

var identifier = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
var version = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:-[a-zA-Z0-9.-]+)?$`)
var permissionAction = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}(?:\.[a-z][a-z0-9-]{0,62})*$`)

func validPermission(module, permission string) bool {
	return strings.HasPrefix(permission, module+":") && permissionAction.MatchString(strings.TrimPrefix(permission, module+":"))
}

type Route struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Permission string `json:"permission"`
}
type Frontend struct {
	Route      string `json:"route"`
	Permission string `json:"permission"`
	Entry      string `json:"entry"`
}
type Descriptor struct {
	ID           string     `json:"id"`
	Version      string     `json:"version"`
	APIVersion   int        `json:"api_version"`
	Dependencies []string   `json:"dependencies,omitempty"`
	Capabilities []string   `json:"capabilities,omitempty"`
	Routes       []Route    `json:"routes,omitempty"`
	FlowNodes    []string   `json:"flow_nodes,omitempty"`
	Frontend     []Frontend `json:"frontend,omitempty"`
}

func (d Descriptor) Validate() error {
	if !identifier.MatchString(d.ID) || !version.MatchString(d.Version) || d.APIVersion != APIVersion {
		return fmt.Errorf("invalid extension identity or API: %q", d.ID)
	}
	for _, dep := range d.Dependencies {
		if !identifier.MatchString(dep) || dep == d.ID {
			return fmt.Errorf("invalid dependency for %s", d.ID)
		}
	}
	for _, r := range d.Routes {
		decoded, err := url.PathUnescape(r.Path)
		if err != nil || decoded != r.Path || strings.ContainsAny(r.Path, "\\?#") || path.Clean(r.Path) != r.Path || !strings.HasPrefix(r.Path, "/api/extensions/"+d.ID+"/") || !validPermission(d.ID, r.Permission) {
			return fmt.Errorf("invalid extension route for %s", d.ID)
		}
		switch r.Method {
		case "GET", "POST", "PUT", "PATCH", "DELETE":
		default:
			return fmt.Errorf("invalid route method")
		}
	}
	for _, n := range d.FlowNodes {
		if !strings.HasPrefix(n, d.ID+":") || !identifier.MatchString(strings.TrimPrefix(n, d.ID+":")) {
			return fmt.Errorf("invalid flow node namespace")
		}
	}
	for _, f := range d.Frontend {
		if !strings.HasPrefix(f.Route, "/extensions/"+d.ID+"/") || path.Clean(f.Route) != f.Route || strings.ContainsAny(f.Route, "%\\?#") || !validPermission(d.ID, f.Permission) || f.Entry == "" {
			return fmt.Errorf("invalid frontend namespace")
		}
	}
	return nil
}

type Registry struct {
	mu      sync.RWMutex
	modules map[string]Descriptor
}

func NewRegistry() *Registry { return &Registry{modules: map[string]Descriptor{}} }
func clone(d Descriptor) Descriptor {
	d.Dependencies = append([]string(nil), d.Dependencies...)
	d.Capabilities = append([]string(nil), d.Capabilities...)
	d.Routes = append([]Route(nil), d.Routes...)
	d.FlowNodes = append([]string(nil), d.FlowNodes...)
	d.Frontend = append([]Frontend(nil), d.Frontend...)
	return d
}
func (r *Registry) Register(d Descriptor) error {
	if err := d.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.modules[d.ID]; exists {
		return fmt.Errorf("duplicate extension %s", d.ID)
	}
	r.modules[d.ID] = clone(d)
	return nil
}
func (r *Registry) Manifest() []Descriptor {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]Descriptor, 0, len(r.modules))
	for _, d := range r.modules {
		result = append(result, clone(d))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
func (r *Registry) Validate(required []string) error {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, id := range required {
		if _, ok := r.modules[id]; !ok {
			return fmt.Errorf("required extension %s: %w", id, ErrUnavailable)
		}
	}
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("extension dependency cycle at %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		d, ok := r.modules[id]
		if !ok {
			return fmt.Errorf("missing dependency %s", id)
		}
		state[id] = 1
		for _, dep := range d.Dependencies {
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range r.modules {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
