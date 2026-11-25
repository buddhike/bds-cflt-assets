package main

import "testing"

func TestAgentMustListenOnLoopback(t *testing.T) {
	for addr, ok := range map[string]bool{
		"127.0.0.1:8081": true,
		"localhost:8081": true,
		"[::1]:8081":     true,
		"0.0.0.0:8081":   false,
		":8081":          false,
		"10.0.0.5:8081":  false,
		"nonsense":       false,
	} {
		if err := requireLoopback(addr); (err == nil) != ok {
			t.Errorf("requireLoopback(%q) = %v, want ok=%v", addr, err, ok)
		}
	}
}
