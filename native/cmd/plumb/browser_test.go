package main

import (
	"reflect"
	"testing"
)

func envOf(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestBrowserCommand(t *testing.T) {
	const url = "http://localhost:8000"
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		wantOK   bool
		wantName string
		wantArgs []string
	}{
		{"windows", "windows", nil, true, "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"macos", "darwin", nil, true, "open", []string{url}},
		{"linux desktop (X11)", "linux", map[string]string{"DISPLAY": ":0"}, true, "xdg-open", []string{url}},
		{"linux desktop (Wayland)", "linux", map[string]string{"WAYLAND_DISPLAY": "wayland-0"}, true, "xdg-open", []string{url}},
		{"linux headless server", "linux", nil, false, "", nil},
		{"disabled via env", "windows", map[string]string{"PLUMB_NO_BROWSER": "1"}, false, "", nil},
		{"disabled via env (any truthy value)", "darwin", map[string]string{"PLUMB_NO_BROWSER": "true"}, false, "", nil},
		{"env explicitly false still opens", "windows", map[string]string{"PLUMB_NO_BROWSER": "false"}, true, "rundll32", []string{"url.dll,FileProtocolHandler", url}},
		{"env 0 still opens", "darwin", map[string]string{"PLUMB_NO_BROWSER": "0"}, true, "open", []string{url}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, args, ok := browserCommand(tt.goos, url, envOf(tt.env))
			if ok != tt.wantOK || name != tt.wantName || !reflect.DeepEqual(args, tt.wantArgs) {
				t.Errorf("browserCommand(%s, %v) = %q %v ok=%v; want %q %v ok=%v", tt.goos, tt.env, name, args, ok, tt.wantName, tt.wantArgs, tt.wantOK)
			}
		})
	}
}
