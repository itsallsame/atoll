package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadPasswordFileRequiresPrivateRegularFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "password")
	if err := os.WriteFile(path, []byte("local-password\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if password, err := readPasswordFile(path); err != nil || password != "local-password" {
		t.Fatalf("password=%q err=%v", password, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readPasswordFile(path); err == nil {
		t.Fatal("world-readable password file was accepted")
	}
}

func TestBridgeListenAddressMustBeExplicitLoopback(t *testing.T) {
	for _, address := range []string{"127.0.0.1:0", "[::1]:4321"} {
		if err := validateListenAddress(address); err != nil {
			t.Fatalf("loopback address %q: %v", address, err)
		}
	}
	for _, address := range []string{":8080", "0.0.0.0:8080", "localhost:8080", "example.test:8080"} {
		if err := validateListenAddress(address); err == nil {
			t.Fatalf("non-explicit loopback address %q was accepted", address)
		}
	}
}
