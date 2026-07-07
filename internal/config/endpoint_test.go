package config

import "testing"

func TestIsLoopbackEndpoint(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		want    bool
	}{
		{"localhost with port (lmstudio seed)", "http://localhost:1234", true},
		{"127.0.0.1 with port", "http://127.0.0.1:4000", true},
		{"127.0.0.0/8 range member", "http://127.0.0.2:8080", true},
		{"IPv6 loopback bracketed", "http://[::1]:8080", true},
		{"https loopback", "https://localhost", true},
		{"non-loopback hostname", "https://api.example.com", false},
		{"bare non-loopback host", "http://x:4000", false},
		{"non-loopback public IP", "http://8.8.8.8:4000", false},
		{"private but non-loopback IP", "http://10.0.0.5:4000", false},
		{"empty string", "", false},
		{"malformed url", "://not a url", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsLoopbackEndpoint(tc.baseURL); got != tc.want {
				t.Errorf("IsLoopbackEndpoint(%q) = %v, want %v", tc.baseURL, got, tc.want)
			}
		})
	}
}
