package browserbroker

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFileProfileResolverFencesVersionDomainAndConcurrentLease(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "chrome-profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(root, "profiles.json")
	writeProfileRegistryForTest(t, registryPath, ProfileRegistryEntry{ProfileID: "profile/a", ProfileVersion: 7,
		SecurityDomain: "jobs.example.test", UserDataDir: profileDir, ProfileDirectory: "Profile 1"})
	resolver, err := NewFileProfileResolver(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	lease, err := resolver.Resolve(context.Background(), "profile://recruiting/profile%2Fa", 7,
		"https://jobs.example.test/openings")
	if err != nil || lease.UserDataDir != profileDir || lease.ProfileDirectory != "Profile 1" {
		t.Fatalf("Profile lease=%+v err=%v", lease, err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := resolver.Resolve(blockedCtx, "profile://recruiting/profile%2Fa", 7,
		"https://jobs.example.test/openings"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("concurrent Profile lease was not serialized: %v", err)
	}
	lease.Release()
	if _, err := resolver.Resolve(context.Background(), "profile://recruiting/profile%2Fa", 6,
		"https://jobs.example.test/openings"); err == nil {
		t.Fatal("stale local Profile version was accepted")
	}
	if _, err := resolver.Resolve(context.Background(), "profile://recruiting/profile%2Fa", 7,
		"https://other.example.test/openings"); err == nil {
		t.Fatal("cross-domain Profile use was accepted")
	}

	// Registry changes are observed without restarting the executor.
	writeProfileRegistryForTest(t, registryPath, ProfileRegistryEntry{ProfileID: "profile/a", ProfileVersion: 8,
		SecurityDomain: "jobs.example.test", UserDataDir: profileDir})
	rotated, err := resolver.Resolve(context.Background(), "profile://recruiting/profile%2Fa", 8,
		"https://jobs.example.test/openings")
	if err != nil || rotated.ProfileDirectory != "Default" {
		t.Fatalf("rotated Profile lease=%+v err=%v", rotated, err)
	}
	rotated.Release()
}

func TestFileProfileResolverAuthenticatesBindingAndAtomicallyAdvancesVersion(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "chrome-profile")
	if err := os.Mkdir(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("profile-binding-secret-", 2)
	digest := sha256.Sum256([]byte(token))
	registryPath := filepath.Join(root, "profiles.json")
	writeProfileRegistryForTest(t, registryPath, ProfileRegistryEntry{ProfileID: "profile-1", ProfileVersion: 1,
		SecurityDomain: "jobs.example.test", UserDataDir: profileDir, BindingTokenHash: "sha256:" + fmt.Sprintf("%x", digest[:])})
	resolver, err := NewFileProfileResolver(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.AuthenticateBinding("profile-1", token); err != nil {
		t.Fatal(err)
	}
	if err := resolver.AuthenticateBinding("profile-1", strings.Repeat("wrong-binding-secret-", 2)); err == nil {
		t.Fatal("wrong Profile binding token was accepted")
	}
	bound, err := resolver.AcquireBoundProfile(context.Background(), "profile-1", token)
	if err != nil {
		t.Fatal(err)
	}
	otherProcess, err := NewFileProfileResolver(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	blockedCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	if lease, err := otherProcess.Resolve(blockedCtx, "profile://recruiting/profile-1", 1,
		"https://jobs.example.test/private"); !errors.Is(err, context.DeadlineExceeded) {
		if lease != nil {
			lease.Release()
		}
		t.Fatalf("cross-process Profile lease was not serialized: %v", err)
	}
	cancel()
	bound.Release()
	if err := resolver.AdvanceVersion("profile-1", "jobs.example.test", []uint64{1}, 3); err != nil {
		t.Fatal(err)
	}
	// Result retries are idempotent, while a stale or cross-domain writer is fenced.
	if err := resolver.AdvanceVersion("profile-1", "jobs.example.test", []uint64{1}, 3); err != nil {
		t.Fatal(err)
	}
	if err := resolver.AdvanceVersion("profile-1", "other.example.test", []uint64{3}, 4); err == nil {
		t.Fatal("cross-domain Profile registry rotation was accepted")
	}
	lease, err := resolver.Resolve(context.Background(), "profile://recruiting/profile-1", 3,
		"https://jobs.example.test/private")
	if err != nil {
		t.Fatal(err)
	}
	lease.Release()
	info, err := os.Stat(registryPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("rotated registry mode=%v", info.Mode().Perm())
	}
}

func TestFileProfileResolverRejectsInsecureRegistryAndProfileDirectory(t *testing.T) {
	root := t.TempDir()
	profileDir := filepath.Join(root, "chrome-profile")
	if err := os.Mkdir(profileDir, 0o755); err != nil {
		t.Fatal(err)
	}
	registryPath := filepath.Join(root, "profiles.json")
	writeProfileRegistryForTest(t, registryPath, ProfileRegistryEntry{ProfileID: "profile-1", ProfileVersion: 1,
		SecurityDomain: "jobs.example.test", UserDataDir: profileDir})
	if _, err := NewFileProfileResolver(registryPath); err == nil {
		t.Fatal("group/world-readable Profile directory was accepted")
	}
	if err := os.Chmod(profileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(registryPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFileProfileResolver(registryPath); err == nil {
		t.Fatal("group/world-readable Profile registry was accepted")
	}
}

func writeProfileRegistryForTest(t *testing.T, path string, entries ...ProfileRegistryEntry) {
	t.Helper()
	raw, err := json.Marshal(profileRegistryDocument{Version: ProfileRegistryVersion, Profiles: entries})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}
