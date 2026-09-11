// Command recruiting-extension-bridge is the loopback-only adapter between
// the optional Chrome extension and Atoll's existing authenticated public
// Resource/Message protocols. It does not host or execute recruiting Work.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/browserbroker"
	"github.com/wanpengxie/atoll/tools/recruiting-extension/bridge"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "recruiting extension bridge:", err)
		os.Exit(1)
	}
}

func run() error {
	var config bridge.AtollConfig
	var passwordFile, executorTokenFile, profileRegistryPath, listenAddress string
	flag.StringVar(&config.BaseURL, "atoll", "http://127.0.0.1:8080", "Atoll HTTPS or loopback HTTP base URL")
	flag.StringVar(&config.Email, "email", "", "ordinary Atoll operator email")
	flag.StringVar(&passwordFile, "password-file", "", "0600 file containing the operator password")
	flag.StringVar(&config.ChannelID, "channel", "", "channel containing the Recruiting Actor (defaults to the operator home)")
	flag.StringVar(&config.ControlActorID, "control-actor", "", "Recruiting Actor ID in the selected channel")
	flag.StringVar(&listenAddress, "listen", "127.0.0.1:0", "loopback listen address")
	flag.StringVar(&executorTokenFile, "executor-token-file", "", "optional 0400/0600 token file enabling the local Profile executor endpoint")
	flag.StringVar(&profileRegistryPath, "profile-registry", "", "owner-only local browser Profile registry used by repair and daily execution")
	flag.Parse()
	if err := validateListenAddress(listenAddress); err != nil {
		return err
	}
	password, err := readPasswordFile(passwordFile)
	if err != nil {
		return err
	}
	config.Password, config.Timeout = password, 30*time.Second
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := bridge.LoginAndConnect(ctx, config)
	config.Password, password = "", ""
	if err != nil {
		return err
	}
	defer client.Close()
	tokenBytes := make([]byte, 32)
	if _, err := rand.Read(tokenBytes); err != nil {
		return fmt.Errorf("generate pairing token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(tokenBytes)
	var executorToken string
	var profileRegistry *browserbroker.FileProfileResolver
	if executorTokenFile != "" {
		executorToken, err = readPasswordFile(executorTokenFile)
		if err != nil {
			return fmt.Errorf("read executor token: %w", err)
		}
		if len(executorToken) < 32 {
			return fmt.Errorf("executor token must contain at least 32 characters")
		}
		profileRegistry, err = browserbroker.NewFileProfileResolver(profileRegistryPath)
		if err != nil {
			return fmt.Errorf("open browser Profile registry: %w", err)
		}
	} else if strings.TrimSpace(profileRegistryPath) != "" {
		return fmt.Errorf("profile-registry requires executor-token-file")
	}
	service := &bridge.Service{Client: client, Token: token, ExecutorToken: executorToken,
		ProfileRegistry: profileRegistry, Now: time.Now}
	handler, err := service.Handler()
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", listenAddress)
	if err != nil {
		return fmt.Errorf("listen on recruiting extension bridge: %w", err)
	}
	defer listener.Close()
	address := listener.Addr().(*net.TCPAddr)
	pairing := map[string]any{"version": bridge.BridgeProtocolVersion,
		"endpoint": fmt.Sprintf("ws://127.0.0.1:%d/capture", address.Port), "token": token}
	if executorToken != "" {
		pairing["browser_broker_url"] = fmt.Sprintf("http://127.0.0.1:%d", address.Port)
	}
	encoded, _ := json.Marshal(pairing)
	fmt.Println(string(encoded))
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- server.Serve(listener) }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	case err := <-serveErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func readPasswordFile(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("password-file is required")
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("stat password file: %w", err)
	}
	if !info.Mode().IsRegular() || (info.Mode().Perm() != 0o600 && info.Mode().Perm() != 0o400) || info.Size() == 0 || info.Size() > 4096 {
		return "", fmt.Errorf("password file must be a non-empty regular file no larger than 4096 bytes with mode 0400 or 0600")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read password file: %w", err)
	}
	password := strings.TrimSpace(string(raw))
	if password == "" || strings.ContainsRune(password, '\x00') {
		return "", fmt.Errorf("password file is empty or invalid")
	}
	return password, nil
}

func validateListenAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("listen must be a loopback host:port: %w", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("recruiting extension bridge may listen only on an explicit loopback IP")
	}
	return nil
}
