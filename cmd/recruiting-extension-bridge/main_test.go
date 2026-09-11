package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserbroker"
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

func TestProvisionLocalProfileCreatesPrivateSlotWithoutPrintingToken(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	registry := filepath.Join(root, "profiles.json")
	userDataDir := filepath.Join(root, "chrome-profile")
	tokenFile := filepath.Join(root, "profile-binding-token")
	if err := provisionLocalProfile(registry, "profile-a", "JOBS.EXAMPLE.TEST", userDataDir, "Default", tokenFile); err != nil {
		t.Fatal(err)
	}
	tokenInfo, err := os.Stat(tokenFile)
	if err != nil || tokenInfo.Mode().Perm() != 0o600 {
		t.Fatalf("binding token file info=%v err=%v", tokenInfo, err)
	}
	raw, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	token := string(raw[:len(raw)-1])
	if len(token) < 32 {
		t.Fatal("generated Profile binding token is too short")
	}
	digest := sha256.Sum256([]byte(token))
	resolver, err := browserbroker.NewFileProfileResolver(registry)
	if err != nil || resolver.AuthenticateBinding("profile-a", token) != nil {
		t.Fatalf("provisioned registry cannot authenticate binding hash=%s err=%v", fmt.Sprintf("sha256:%x", digest[:]), err)
	}
	if err := provisionLocalProfile(registry, "profile-a", "jobs.example.test", userDataDir, "Default", tokenFile); err == nil {
		t.Fatal("provisioning overwrote an existing binding token file")
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
