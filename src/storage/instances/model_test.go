package instances

import "testing"

func TestInstanceNormalization(t *testing.T) {
	in := CreateInput{Name: " Shared ", ConsoleURL: "https://CONSOLE.EXAMPLE:443/", BackendURL: "http://backend.namespace.svc:8080"}
	if err := in.Normalize(); err != nil || in.ConsoleURL != "https://console.example" {
		t.Fatal("instance was not canonicalized")
	}
	for _, url := range []string{"http://console.example", "https://user:password@console.example", "https://console.example/path", "https://console.example?query=1", "https://console.example:0", "https://console.example:65536", "https://console.example:", "https://*.example", "https://[not-an-ip]"} {
		in.ConsoleURL = url
		if err := in.Normalize(); err == nil {
			t.Fatal("unsafe console origin was accepted")
		}
	}
	if origin, err := Origin("http://[::1]:3000/", true); err != nil || origin != "http://[::1]:3000" {
		t.Fatal("local IPv6 console origin was not preserved")
	}
}
