package main

import "testing"

func TestLoadEndpointRejectsExternalAndCredentialedHosts(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:19201", "http://localhost:19201", "http://[::1]:19201"} {
		if err := loopbackEndpoint(endpoint); err != nil {
			t.Fatalf("valid loopback: %v", err)
		}
	}
	for _, endpoint := range []string{"", "https://127.0.0.1:19201", "http://example.com", "http://localhost.example.com", "http://10.20.0.231:9200", "http://user:secret@127.0.0.1", "http://127.0.0.1/items", "http://127.0.0.1?token=x", "http://127.0.0.1#fragment"} {
		if err := loopbackEndpoint(endpoint); err == nil {
			t.Fatalf("unsafe load endpoint accepted: %s", endpoint)
		}
	}
}
