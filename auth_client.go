package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strings"
	"time"

	pb "atlas/rs/gen/rs"
	"atlas/rs/internal/protocol"
	"golang.org/x/term"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
)

const authTimeout = 15 * time.Second

// httpClient serves login and claim requests. It dials directly, like the
// gRPC channel, so the recorded peer IP is the node itself rather than a
// proxy, and it refuses redirects so credentials never leave the origin.
var httpClient = &http.Client{
	Transport: &http.Transport{
		Proxy:               nil,
		DialContext:         (&net.Dialer{Timeout: dialTimeout}).DialContext,
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		TLSHandshakeTimeout: dialTimeout,
		ForceAttemptHTTP2:   true,
	},
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("redirects are not allowed")
	},
}

// authenticateNode logs in with the node password and returns the session
// together with the IP of the peer that answered.
func authenticateNode(ctx context.Context, node Node) (protocol.SessionResponse, string, error) {
	var session protocol.SessionResponse
	payload, err := json.Marshal(map[string]string{"password": node.Password})
	if err != nil {
		return session, "", err
	}
	var peerIP string
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn: func(info httptrace.GotConnInfo) {
			peerIP = addressIP(info.Conn.RemoteAddr())
		},
	})
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(node.URL, "/")+"/session", bytes.NewReader(payload))
	if err != nil {
		return session, "", err
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := httpClient.Do(request)
	if err != nil {
		return session, "", fmt.Errorf("login: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return session, "", responseError(response)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&session); err != nil {
		return session, "", fmt.Errorf("decode login response: %w", err)
	}
	if session.Token == "" || session.NodeID == "" {
		return session, "", errors.New("login response is incomplete")
	}
	if node.ID != "" && session.NodeID != node.ID {
		return session, "", fmt.Errorf("node ID mismatch: saved %s, server %s", node.ID, session.NodeID)
	}
	if !versionsCompatible(version, session.Version) {
		return session, "", fmt.Errorf("incompatible rs versions: client %s, server %s", version, session.Version)
	}
	if peerIP == "" {
		return session, "", errors.New("login connection has no peer IP")
	}
	return session, peerIP, nil
}

// verifyNode logs in once and fills the server-reported node ID and the
// observed peer IP.
func verifyNode(node *Node) error {
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	session, ip, err := authenticateNode(ctx, *node)
	if err != nil {
		return err
	}
	node.ID, node.IP = session.NodeID, ip
	return nil
}

func dialNode(node Node) (*grpc.ClientConn, pb.RsClient, context.Context, error) {
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	session, _, err := authenticateNode(ctx, node)
	if err != nil {
		return nil, nil, nil, err
	}

	u, err := url.Parse(node.URL)
	if err != nil {
		return nil, nil, nil, err
	}
	target := u.Host
	if u.Port() == "" {
		if u.Scheme == "https" {
			target = net.JoinHostPort(u.Hostname(), "443")
		} else {
			target = net.JoinHostPort(u.Hostname(), "80")
		}
	}

	var transport credentials.TransportCredentials
	if u.Scheme == "https" {
		transport = credentials.NewTLS(&tls.Config{
			MinVersion: tls.VersionTLS12,
			ServerName: u.Hostname(),
		})
	} else {
		transport = insecure.NewCredentials()
	}
	conn, err := grpc.NewClient(target,
		grpc.WithTransportCredentials(transport),
		grpc.WithContextDialer(func(ctx context.Context, addr string) (net.Conn, error) {
			return (&net.Dialer{Timeout: dialTimeout}).DialContext(ctx, "tcp", addr)
		}),
	)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("dial %s: %w", target, err)
	}
	rpcContext := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer "+session.Token)
	return conn, pb.NewRsClient(conn), rpcContext, nil
}

func versionsCompatible(clientVersion, serverVersion string) bool {
	clientMajor, _, clientOK := strings.Cut(clientVersion, ".")
	serverMajor, _, serverOK := strings.Cut(serverVersion, ".")
	return clientOK && serverOK && clientMajor == serverMajor
}

func readSecret(prompt string) (string, error) {
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, prompt)
		value, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(value)), nil
	}
	value, err := io.ReadAll(io.LimitReader(os.Stdin, 2048))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(value)), nil
}

func generatePassword() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func responseError(response *http.Response) error {
	var body struct {
		Error string `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&body)
	if body.Error == "" {
		body.Error = response.Status
	}
	return errors.New(body.Error)
}
