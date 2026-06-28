package web

import (
	"io/fs"
	"testing"
)

// The Floor view assets must be embedded in the binary (CWD-independent serving).
func TestStatic_BundlesFloorView(t *testing.T) {
	want := []string{"index.html", "app.js", "styles/variables.css", "styles/main.css", "assets/logo.svg"}
	sfs := Static()
	for _, p := range want {
		if _, err := fs.Stat(sfs, p); err != nil {
			t.Errorf("embedded static FS missing %q: %v", p, err)
		}
	}
}
