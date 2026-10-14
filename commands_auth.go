package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"atlas/rs/internal/fsx"
	"atlas/rs/internal/protocol"
	"atlas/rs/server"
)

const maxClaimBody = 64 << 10

func cmdInit(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	endpoint := fs.String("endpoint", "", "public HTTPS origin, e.g. https://rs.example.com")
	name := fs.String("name", "", "node name included in the one-time credential bundle")
	stateFile := fs.String("state", ".rs-auth.json", "server authentication state file")
	claimURLFile := fs.String("claim-url-file", "", "write the one-time claim URL to a private file")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *endpoint == "" || *name == "" {
		fmt.Fprintln(os.Stderr, "error: --endpoint and --name are required")
		return 1
	}
	if *claimURLFile != "" && sameFilePath(*claimURLFile, *stateFile) {
		fmt.Fprintln(os.Stderr, "error: claim URL file and auth state must use different paths")
		return 1
	}

	password, err := generatePassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: create password: %v\n", err)
		return 1
	}
	_, claimURL, err := server.InitializeAuth(*stateFile, *endpoint, *name, password)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if *claimURLFile != "" {
		if err := fsx.WriteFileAtomic(*claimURLFile, []byte(claimURL+"\n"), 0o600, false); err != nil {
			// The link was never shown, so the fresh state is unusable.
			_ = os.Remove(*stateFile)
			fmt.Fprintf(os.Stderr, "error: save claim URL: %v\n", err)
			return 1
		}
		fmt.Printf("One-time claim URL saved to %s\n", *claimURLFile)
		return 0
	}
	fmt.Printf("One-time claim URL: %s\n", claimURL)
	return 0
}

func cmdClaim(args []string) int {
	fs := flag.NewFlagSet("claim", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "error: usage: rs claim [one-time-url]")
		return 1
	}

	var claimURL string
	var err error
	if fs.NArg() == 1 {
		claimURL = fs.Arg(0)
	} else {
		claimURL, err = readSecret("One-time claim URL: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read claim URL: %v\n", err)
			return 1
		}
	}
	claimToken, err := validateClaimURL(claimURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	// The link is consumed by the request below, so make sure the result can
	// be stored before spending it.
	if err := preflightPrivateWrite(nodeFile()); err != nil {
		fmt.Fprintf(os.Stderr, "error: node file is not writable: %v\n", err)
		return 1
	}
	if _, err := loadNodes(); err != nil {
		fmt.Fprintf(os.Stderr, "error: node file is not readable: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	bundle, ip, err := requestClaim(ctx, claimURL, claimToken)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: claim credentials: %v\n", err)
		return 1
	}
	node := bundle.Node
	node.URL = strings.TrimRight(node.URL, "/")
	node.IP = ip
	if err := validateClaimNode(node); err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid claimed node: %v\n", err)
		return 1
	}
	if err := upsertNodes(node); err != nil {
		fmt.Fprintf(os.Stderr, "error: save claimed node: %v\n", err)
		return 1
	}
	fmt.Printf("claimed node %s (%s)\n", node.Name, node.IP)
	return 0
}

func requestClaim(ctx context.Context, claimURL, claimToken string) (protocol.ClaimBundle, string, error) {
	var peerIP string
	trace := &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			peerIP = addressIP(info.Conn.RemoteAddr())
		},
	}
	request, err := http.NewRequestWithContext(
		httptrace.WithClientTrace(ctx, trace),
		http.MethodPost,
		claimURL,
		nil,
	)
	if err != nil {
		return protocol.ClaimBundle{}, "", errors.New("create request")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return protocol.ClaimBundle{}, "", errors.New(strings.ReplaceAll(err.Error(), claimToken, "[redacted]"))
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return protocol.ClaimBundle{}, "", responseError(response)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, maxClaimBody+1))
	decoder.DisallowUnknownFields()
	var envelope protocol.ClaimEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		return protocol.ClaimBundle{}, "", fmt.Errorf("decode response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return protocol.ClaimBundle{}, "", errors.New("claim response must contain one JSON value")
	}
	bundle, err := protocol.OpenClaim(envelope, claimToken)
	if err != nil {
		return protocol.ClaimBundle{}, "", fmt.Errorf("decrypt response: %w", err)
	}
	if peerIP == "" {
		return protocol.ClaimBundle{}, "", errors.New("claim connection has no peer IP")
	}
	return bundle, peerIP, nil
}

func validateClaimURL(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("claim URL is invalid")
	}
	if parsed.Scheme != "https" && (parsed.Scheme != "http" || !isLoopbackHost(parsed.Hostname())) {
		return "", errors.New("claim URL must use HTTPS")
	}
	parts := strings.Split(strings.Trim(parsed.EscapedPath(), "/"), "/")
	if len(parts) != 2 || parts[0] != "claim" {
		return "", errors.New("claim URL must match https://host/claim/<token>")
	}
	token, err := url.PathUnescape(parts[1])
	if err != nil {
		return "", errors.New("claim URL token is invalid")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil || len(decoded) != 32 {
		return "", errors.New("claim URL token is invalid")
	}
	return token, nil
}

func validateClaimNode(node Node) error {
	if err := validateNode(node); err != nil {
		return err
	}
	if net.ParseIP(node.IP) == nil {
		return errors.New("claim connection returned an invalid peer IP")
	}
	return nil
}

func cmdCredential(args []string) int {
	if len(args) == 0 || args[0] != "reset" {
		fmt.Fprintln(os.Stderr, "error: usage: rs credential reset [--state FILE] [--name NAME] [--claim-url-file FILE]")
		return 1
	}
	fs := flag.NewFlagSet("credential reset", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	stateFile := fs.String("state", ".rs-auth.json", "server authentication state file")
	name := fs.String("name", "", "replace the node name stored in the auth state")
	claimURLFile := fs.String("claim-url-file", "", "write the one-time claim URL to a private file")
	if err := fs.Parse(args[1:]); err != nil {
		return 1
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "error: usage: rs credential reset [--state FILE] [--name NAME] [--claim-url-file FILE]")
		return 1
	}
	if *claimURLFile != "" && sameFilePath(*claimURLFile, *stateFile) {
		fmt.Fprintln(os.Stderr, "error: claim URL file and auth state must use different paths")
		return 1
	}
	password, err := generatePassword()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: create password: %v\n", err)
		return 1
	}
	_, claimURL, err := server.ResetAuth(*stateFile, *name, password, *claimURLFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: reset credentials: %v\n", err)
		return 1
	}
	if *claimURLFile != "" {
		fmt.Printf("One-time claim URL saved to %s\n", *claimURLFile)
	} else {
		fmt.Printf("One-time claim URL: %s\n", claimURL)
	}
	fmt.Println("Restart the rs service before claiming the new credentials.")
	return 0
}

func addressIP(address net.Addr) string {
	if address == nil {
		return ""
	}
	host, _, err := net.SplitHostPort(address.String())
	if err != nil {
		host = address.String()
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return ""
	}
	return ip.String()
}

func preflightPrivateWrite(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".rs-check-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func sameFilePath(a, b string) bool {
	absA, errA := filepath.Abs(a)
	absB, errB := filepath.Abs(b)
	return errA == nil && errB == nil && absA == absB
}
