package registry

import "testing"

func TestStarterIsValid(t *testing.T) {
	known := func(s string) bool { return s == "greenhouse" || s == "lever" || s == "ashby" }
	es, err := Starter(known)
	if err != nil {
		t.Fatal(err)
	}
	if len(es) < 50 {
		t.Errorf("registry has %d companies", len(es))
	}
}
