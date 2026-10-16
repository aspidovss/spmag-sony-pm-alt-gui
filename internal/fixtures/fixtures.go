// Package fixtures locates camera captures in testdata/<model>/ (made with
// "wpr capture") for tests.
package fixtures

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// Root returns the repository's testdata directory.
func Root() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..", "testdata")
}

// Capture is one camera's capture directory.
type Capture struct {
	Model string
	Dir   string
}

// Read returns a fixture file, or nil if the capture lacks it.
func (c Capture) Read(name string) []byte {
	b, err := os.ReadFile(filepath.Join(c.Dir, name))
	if err != nil {
		return nil
	}
	return b
}

// All lists captures; the test is skipped if there are none.
func All(t *testing.T) []Capture {
	t.Helper()
	entries, _ := os.ReadDir(Root())
	var out []Capture
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, Capture{Model: e.Name(), Dir: filepath.Join(Root(), e.Name())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Model < out[j].Model })
	if len(out) == 0 {
		t.Skip("no camera captures in testdata/ (create one with: wpr capture)")
	}
	return out
}
