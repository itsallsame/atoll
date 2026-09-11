package browserbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const ProfileRegistryVersion = "recruiting.browser-profile-registry.v1"

type ProfileRegistryEntry struct {
	ProfileID        string `json:"profile_id"`
	ProfileVersion   uint64 `json:"profile_version"`
	SecurityDomain   string `json:"security_domain"`
	UserDataDir      string `json:"user_data_dir"`
	ProfileDirectory string `json:"profile_directory,omitempty"`
}

type profileRegistryDocument struct {
	Version  string                 `json:"version"`
	Profiles []ProfileRegistryEntry `json:"profiles"`
}

// ProfileLease is a local authorization to use one Chrome profile directory.
// Release must be called; it serializes all sessions for the same Profile.
type ProfileLease struct {
	UserDataDir      string
	ProfileDirectory string
	release          func()
}

func (l *ProfileLease) Release() {
	if l != nil && l.release != nil {
		l.release()
		l.release = nil
	}
}

type ProfileResolver interface {
	Resolve(context.Context, string, uint64, string) (*ProfileLease, error)
}

// FileProfileResolver reloads a small, owner-only registry for each lease so
// a repaired Profile can rotate versions without restarting the Executor.
// The registry contains local paths, never cookies, passwords, or OTP values.
type FileProfileResolver struct {
	path  string
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func NewFileProfileResolver(path string) (*FileProfileResolver, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) {
		return nil, errors.New("browser Profile registry path must be absolute")
	}
	if _, err := readProfileRegistry(path); err != nil {
		return nil, err
	}
	return &FileProfileResolver{path: filepath.Clean(path), locks: make(map[string]chan struct{})}, nil
}

func (r *FileProfileResolver) Resolve(ctx context.Context, profileRef string, profileVersion uint64,
	endpointURL string) (*ProfileLease, error) {
	if r == nil || ctx == nil || profileVersion == 0 {
		return nil, errors.New("Profile lease requires resolver, context, and version")
	}
	profileID, err := parseProfileReference(profileRef)
	if err != nil {
		return nil, err
	}
	entries, err := readProfileRegistry(r.path)
	if err != nil {
		return nil, err
	}
	entry, found := entries[profileID]
	if !found || entry.ProfileVersion != profileVersion {
		return nil, errors.New("browser Profile is absent or its local version is stale")
	}
	endpoint, err := url.Parse(strings.TrimSpace(endpointURL))
	if err != nil || !strings.EqualFold(endpoint.Hostname(), entry.SecurityDomain) {
		return nil, errors.New("browser Profile security domain does not match the endpoint")
	}
	lock := r.profileLock(profileID)
	select {
	case lock <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return &ProfileLease{UserDataDir: entry.UserDataDir, ProfileDirectory: entry.ProfileDirectory,
		release: func() { <-lock }}, nil
}

func (r *FileProfileResolver) profileLock(profileID string) chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	lock := r.locks[profileID]
	if lock == nil {
		lock = make(chan struct{}, 1)
		r.locks[profileID] = lock
	}
	return lock
}

func readProfileRegistry(path string) (map[string]ProfileRegistryEntry, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("stat browser Profile registry: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		(info.Mode().Perm() != 0o400 && info.Mode().Perm() != 0o600) || info.Size() < 2 || info.Size() > 1<<20 {
		return nil, errors.New("browser Profile registry must be a 0400/0600 regular file no larger than one MiB")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read browser Profile registry: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var document profileRegistryDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode browser Profile registry: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("browser Profile registry has trailing JSON")
	}
	if document.Version != ProfileRegistryVersion || len(document.Profiles) == 0 || len(document.Profiles) > 10_000 {
		return nil, errors.New("browser Profile registry version or bounded profile list is invalid")
	}
	result := make(map[string]ProfileRegistryEntry, len(document.Profiles))
	for index, entry := range document.Profiles {
		entry.ProfileID = strings.TrimSpace(entry.ProfileID)
		entry.SecurityDomain = strings.ToLower(strings.TrimSpace(entry.SecurityDomain))
		entry.UserDataDir = filepath.Clean(strings.TrimSpace(entry.UserDataDir))
		entry.ProfileDirectory = strings.TrimSpace(entry.ProfileDirectory)
		if entry.ProfileDirectory == "" {
			entry.ProfileDirectory = "Default"
		}
		if entry.ProfileID == "" || entry.ProfileVersion == 0 || entry.SecurityDomain == "" ||
			strings.ContainsAny(entry.SecurityDomain, "/:@?# \t\r\n") || !filepath.IsAbs(entry.UserDataDir) ||
			entry.UserDataDir == string(filepath.Separator) || entry.ProfileDirectory == "." || entry.ProfileDirectory == ".." ||
			len(entry.ProfileDirectory) > 128 || strings.ContainsAny(entry.ProfileDirectory, "/\\\\\x00\r\n") {
			return nil, fmt.Errorf("browser Profile registry entry %d is invalid", index)
		}
		directoryInfo, err := os.Stat(entry.UserDataDir)
		if err != nil || !directoryInfo.IsDir() || directoryInfo.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("browser Profile directory %d must exist and be owner-only", index)
		}
		resolved, err := filepath.EvalSymlinks(entry.UserDataDir)
		if err != nil || resolved != entry.UserDataDir {
			return nil, fmt.Errorf("browser Profile directory %d must be a canonical non-symlink path", index)
		}
		if _, duplicate := result[entry.ProfileID]; duplicate {
			return nil, fmt.Errorf("browser Profile registry repeats profile_id %q", entry.ProfileID)
		}
		result[entry.ProfileID] = entry
	}
	return result, nil
}

func parseProfileReference(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "profile" || parsed.Host != "recruiting" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" || parsed.Path == "/" {
		return "", errors.New("browser Profile reference is invalid")
	}
	escaped := strings.TrimPrefix(parsed.EscapedPath(), "/")
	profileID, err := url.PathUnescape(escaped)
	if err != nil || profileID == "" || strings.TrimSpace(profileID) != profileID || len(profileID) > 191 ||
		strings.ContainsAny(profileID, "\x00\r\n\t") {
		return "", errors.New("browser Profile reference identity is invalid")
	}
	return profileID, nil
}
