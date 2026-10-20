package extensions

import "testing"

func TestCollectionRouteCanonicalNamespace(t *testing.T) {
	d := Descriptor{ID: "cases", Version: "0.1.0", APIVersion: APIVersion, Routes: []Route{{Method: "GET", Path: "/api/extensions/cases", Permission: "cases:read.own"}}}
	if e := d.Validate(); e != nil {
		t.Fatal(e)
	}
	d.Routes[0].Path="/api/extensions/cases/"
	if e:=d.Validate();e!=nil{t.Fatal("canonical collection",e)}
	for _, path := range []string{"/api/extensions/cases-other", "/api/extensions/cases/nested/", "/api/extensions/cases/../other", "/api/extensions/cases%2fother"} {
		d.Routes[0].Path = path
		if d.Validate() == nil {
			t.Fatalf("accepted %s", path)
		}
	}
}
