// Command rs is a gRPC client and server for remote shell and file upload.
// A single binary: `rs serve` runs the server on a remote host; `rs node`,
// `rs shell`, `rs push` and `rs health` drive it from a local host, using the
// node list in ~/.rs/node.json.
package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	pb "atlas/rs/gen/rs"
	"atlas/rs/server"
	"google.golang.org/grpc"
)

// version mirrors the server's Version so `rs serve` and `rs health` agree.
const version = server.Version

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}
	args := os.Args[2:]
	switch os.Args[1] {
	case "init":
		os.Exit(cmdInit(args))
	case "claim":
		os.Exit(cmdClaim(args))
	case "serve":
		os.Exit(cmdServe(args))
	case "node":
		os.Exit(cmdNode(args))
	case "shell":
		os.Exit(cmdShell(args))
	case "push":
		os.Exit(cmdPush(args))
	case "health":
		os.Exit(cmdHealth(args))
	case "credential":
		os.Exit(cmdCredential(args))
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, `rs %s — remote shell and file upload over gRPC

Usage:
  rs init --name NAME --endpoint https://host [--state FILE] [--claim-url-file FILE]
  rs claim [one-time-url]
  rs serve [--listen ADDR] [--state FILE] [--tls-cert FILE --tls-key FILE]
  rs node add <name> <account-url>     (password from stdin or hidden prompt)
  rs node add --json                   (JSON array from stdin)
  rs node list [--json]
  rs node rm <name>
  rs shell <node> "<script>" [--timeout N]
  rs push <node> <local> <remote> [--mkdir] [--overwrite] [--timeout N]
  rs health <node>
  rs credential reset [--state FILE] [--name NAME] [--claim-url-file FILE]
`, version)
}

// readScript returns the script from positional args, or from stdin (heredoc)
// when no script is passed.
func readScript(args []string) (string, bool) {
	if len(args) > 0 {
		return strings.Join(args, " "), true
	}
	stat, _ := os.Stdin.Stat()
	if (stat.Mode() & os.ModeCharDevice) == 0 {
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", false
		}
		return string(data), true
	}
	return "", false
}

// dialTimeout bounds TCP and TLS handshakes to a node and the health probe;
// shell and push are bounded by their own --timeout.
const dialTimeout = 10 * time.Second

// runShell executes a script and streams output to stdout/stderr, returning
// the remote exit code.
func runShell(ctx context.Context, client pb.RsClient, script string, timeout int32, stdout, stderr io.Writer) (int, error) {
	stream, err := client.Shell(ctx, &pb.ShellRequest{Script: script, TimeoutSeconds: timeout})
	if err != nil {
		return 1, err
	}
	var exitCode int32
	exited := false
	for {
		msg, err := stream.Recv()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 1, err
		}
		if msg.Exited {
			exitCode = msg.ExitCode
			exited = true
			break
		}
		switch msg.Stream {
		case pb.ShellOutput_STDOUT:
			_, _ = stdout.Write(msg.Data)
		case pb.ShellOutput_STDERR:
			_, _ = stderr.Write(msg.Data)
		}
	}
	if !exited {
		// The stream ended without the server's exit-status frame: the
		// connection was cut mid-flight, so a real failure is being reported
		// as success (exit 0) unless we surface an error.
		return 1, errors.New("connection closed before exit status")
	}
	return int(exitCode), nil
}

// runUpload streams one local file to the node, returning bytes written.
func runUpload(ctx context.Context, client pb.RsClient, local, remote string, mkdir, overwrite bool) (int64, error) {
	f, err := os.Open(local)
	if err != nil {
		return 0, fmt.Errorf("open local file: %w", err)
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("stat local file: %w", err)
	}

	stream, err := client.Upload(ctx)
	if err != nil {
		return 0, fmt.Errorf("open upload stream: %w", err)
	}
	if err := stream.Send(&pb.UploadRequest{Payload: &pb.UploadRequest_Header{
		Header: &pb.UploadHeader{
			Path:      remote,
			Size:      stat.Size(),
			Mkdir:     mkdir,
			Overwrite: overwrite,
		},
	}}); err != nil {
		return 0, fmt.Errorf("send header: %w", err)
	}

	buf := make([]byte, 64*1024)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			if err := stream.Send(&pb.UploadRequest{Payload: &pb.UploadRequest_Data{Data: buf[:n]}}); err != nil {
				// The server may have rejected the upload and closed the
				// stream early (e.g. overwrite protection); surface its ack.
				ack, recvErr := stream.CloseAndRecv()
				if recvErr == nil && ack != nil && ack.Error != "" {
					return 0, fmt.Errorf("server: %s", ack.Error)
				}
				return 0, fmt.Errorf("send data: %w", err)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, fmt.Errorf("read local file: %w", err)
		}
	}

	ack, err := stream.CloseAndRecv()
	if err != nil {
		return 0, fmt.Errorf("receive ack: %w", err)
	}
	if ack.Error != "" {
		return 0, fmt.Errorf("server: %s", ack.Error)
	}
	return ack.BytesWritten, nil
}

// ─── subcommands ───

func cmdServe(args []string) int {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listenFlag := fs.String("listen", "127.0.0.1:8999", "listen address")
	stateFlag := fs.String("state", ".rs-auth.json", "authentication state file")
	tlsCertFlag := fs.String("tls-cert", "", "TLS certificate file")
	tlsKeyFlag := fs.String("tls-key", "", "TLS private key file")
	sessionTTLFlag := fs.Duration("session-ttl", 15*time.Minute, "short-lived session lifetime")
	maxUploadFlag := fs.Int64("max-upload-size", 100<<20, "max upload size in bytes (0 = unlimited)")
	_ = fs.Parse(args)

	if (*tlsCertFlag == "") != (*tlsKeyFlag == "") {
		fmt.Fprintln(os.Stderr, "error: --tls-cert and --tls-key must be configured together")
		return 1
	}
	if *maxUploadFlag < 0 {
		fmt.Fprintln(os.Stderr, "error: --max-upload-size cannot be negative")
		return 1
	}
	if *tlsCertFlag == "" && !isLoopbackListen(*listenFlag) {
		fmt.Fprintln(os.Stderr, "error: plaintext serve is restricted to loopback; configure TLS for public listeners")
		return 1
	}
	// The server rewrites the state when a claim is consumed; holding the lock
	// keeps credential reset from racing it.
	unlock, err := server.LockState(*stateFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer unlock()
	auth, err := server.LoadAuthManager(*stateFlag, *sessionTTLFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	lis, err := net.Listen("tcp", *listenFlag)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: listen: %v\n", err)
		return 1
	}

	srv := server.New(server.Config{Auth: auth, MaxUploadSize: *maxUploadFlag})
	gs := grpc.NewServer(
		grpc.MaxRecvMsgSize(1<<20),
		grpc.UnaryInterceptor(srv.UnaryInterceptor()),
		grpc.StreamInterceptor(srv.StreamInterceptor()),
	)
	pb.RegisterRsServer(gs, srv)

	authHandler := http.TimeoutHandler(auth.HTTPHandler(), 15*time.Second, `{"error":"request timed out"}`)
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.ProtoMajor == 2 && strings.HasPrefix(r.Header.Get("Content-Type"), "application/grpc") {
			gs.ServeHTTP(w, r)
			return
		}
		authHandler.ServeHTTP(w, r)
	})
	protocols := new(http.Protocols)
	protocols.SetHTTP1(true)
	protocols.SetHTTP2(true)
	protocols.SetUnencryptedHTTP2(true)
	httpServer := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		TLSConfig:         &tls.Config{MinVersion: tls.VersionTLS12},
		Protocols:         protocols,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		gs.GracefulStop()
	}()

	log.Printf("rs %s server listening on %s", version, *listenFlag)
	if *tlsCertFlag != "" {
		err = httpServer.ServeTLS(lis, *tlsCertFlag, *tlsKeyFlag)
	} else {
		err = httpServer.Serve(lis)
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

func isLoopbackListen(address string) bool {
	host, _, err := net.SplitHostPort(address)
	return err == nil && isLoopbackHost(host)
}

func cmdNode(args []string) int {
	if len(args) == 0 {
		usage()
		return 1
	}
	switch args[0] {
	case "add":
		if len(args) == 2 && args[1] == "--json" {
			nodes, err := decodeNodeJSON(os.Stdin)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			for i := range nodes {
				if err := verifyNode(&nodes[i]); err != nil {
					fmt.Fprintf(os.Stderr, "error: verify node %q: %v\n", nodes[i].Name, err)
					return 1
				}
			}
			if err := upsertNodes(nodes...); err != nil {
				fmt.Fprintf(os.Stderr, "error: %v\n", err)
				return 1
			}
			fmt.Printf("updated %d node(s)\n", len(nodes))
			return 0
		}
		if len(args) != 3 {
			fmt.Fprintln(os.Stderr, "error: usage: rs node add <name> <account-url> | rs node add --json")
			return 1
		}
		password, err := readSecret("Password: ")
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: read password: %v\n", err)
			return 1
		}
		node := Node{Name: args[1], URL: strings.TrimRight(args[2], "/"), Password: password}
		if err := validateNode(node); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if err := verifyNode(&node); err != nil {
			fmt.Fprintf(os.Stderr, "error: verify node: %v\n", err)
			return 1
		}
		if err := upsertNodes(node); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("added node %s (%s)\n", node.Name, node.URL)
		return 0
	case "list":
		jsonOutput := len(args) == 2 && args[1] == "--json"
		if len(args) > 2 || (len(args) == 2 && !jsonOutput) {
			fmt.Fprintln(os.Stderr, "error: usage: rs node list [--json]")
			return 1
		}
		nodes, err := loadNodes()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		if jsonOutput {
			output := make([]nodeListEntry, 0, len(nodes))
			for _, node := range nodes {
				output = append(output, nodeListEntry{Name: node.Name, ID: node.ID, IP: node.IP, URL: node.URL})
			}
			if err := json.NewEncoder(os.Stdout).Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "error: encode nodes: %v\n", err)
				return 1
			}
			return 0
		}
		if len(nodes) == 0 {
			fmt.Println("(no nodes; use `rs node add --json` or `rs node add <name> <account-url>`)")
			return 0
		}
		for _, n := range nodes {
			fmt.Printf("%s\t%s\t%s\n", n.Name, n.IP, n.URL)
		}
		return 0
	case "rm":
		if len(args) != 2 {
			fmt.Fprintln(os.Stderr, "error: usage: rs node rm <name>")
			return 1
		}
		if err := removeNode(args[1]); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			return 1
		}
		fmt.Printf("removed node %s\n", args[1])
		return 0
	default:
		fmt.Fprintf(os.Stderr, "error: unknown node subcommand %q\n", args[0])
		return 1
	}
}

// extractIntFlag extracts --name <value> or --name=<value> from anywhere in
// args and returns the value (or def) with the flag removed. stdlib flag stops
// at the first positional, so scripts placed after the node name would
// otherwise swallow trailing flags.
func extractIntFlag(args []string, name string, def int) (int, []string, error) {
	prefix := "--" + name + "="
	var rest []string
	value := def
	found := false
	for i := 0; i < len(args); i++ {
		if args[i] == "--"+name {
			if found {
				return 0, nil, fmt.Errorf("flag %s may only be specified once", args[i])
			}
			if i+1 >= len(args) {
				return 0, nil, fmt.Errorf("flag %s requires a value", args[i])
			}
			v, err := strconv.Atoi(args[i+1])
			if err != nil {
				return 0, nil, fmt.Errorf("invalid value for %s: %q", args[i], args[i+1])
			}
			value = v
			found = true
			i++
			continue
		}
		if strings.HasPrefix(args[i], prefix) {
			if found {
				return 0, nil, fmt.Errorf("flag --%s may only be specified once", name)
			}
			v, err := strconv.Atoi(strings.TrimPrefix(args[i], prefix))
			if err != nil {
				return 0, nil, fmt.Errorf("invalid value for %s: %q", args[i], strings.TrimPrefix(args[i], prefix))
			}
			value = v
			found = true
			continue
		}
		rest = append(rest, args[i])
	}
	return value, rest, nil
}

func cmdShell(args []string) int {
	timeout, rest, err := extractIntFlag(args, "timeout", 30)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if len(rest) == 0 {
		usage()
		return 1
	}
	if timeout < 1 || int64(timeout) > int64(math.MaxInt32) {
		fmt.Fprintln(os.Stderr, "error: --timeout must be between 1 and 2147483647 seconds")
		return 1
	}
	name := rest[0]
	rest = rest[1:]
	script, ok := readScript(rest)
	if !ok || strings.TrimSpace(script) == "" {
		fmt.Fprintln(os.Stderr, "error: script is required (positional arg or stdin)")
		return 1
	}

	node, err := findNode(name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	conn, client, ctx, err := dialNode(node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer conn.Close()
	// Bound the whole call: script execution plus a grace period for
	// teardown, so a hung server cannot leave the client waiting forever.
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second+30*time.Second)
	defer cancel()

	code, err := runShell(ctx, client, script, int32(timeout), os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	return code
}

// extractBoolFlag removes a boolean --name flag from anywhere in args.
func extractBoolFlag(args []string, name string) (bool, []string) {
	var rest []string
	found := false
	for _, a := range args {
		if !found && a == "--"+name {
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return found, rest
}

func cmdPush(args []string) int {
	mkdir, rest := extractBoolFlag(args, "mkdir")
	overwrite, rest := extractBoolFlag(rest, "overwrite")
	timeout, rest, err := extractIntFlag(rest, "timeout", 300)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	if timeout < 1 || int64(timeout) > int64(math.MaxInt32) {
		fmt.Fprintln(os.Stderr, "error: --timeout must be between 1 and 2147483647 seconds")
		return 1
	}
	if len(rest) != 3 {
		fmt.Fprintln(os.Stderr, "error: usage: rs push <node> <local> <remote> [--mkdir] [--overwrite] [--timeout N]")
		return 1
	}
	node, err := findNode(rest[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	conn, client, ctx, err := dialNode(node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer conn.Close()

	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeout)*time.Second)
	defer cancel()
	written, err := runUpload(ctx, client, rest[1], rest[2], mkdir, overwrite)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s -> %s (%d bytes)\n", rest[1], rest[2], written)
	return 0
}

func cmdHealth(args []string) int {
	if len(args) != 1 {
		usage()
		return 1
	}
	node, err := findNode(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	conn, client, ctx, err := dialNode(node)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	defer conn.Close()

	// Health is a fast unary probe; don't let a dead peer hang the CLI.
	ctx, cancel := context.WithTimeout(ctx, dialTimeout)
	defer cancel()
	resp, err := client.Health(ctx, &pb.HealthRequest{})
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	fmt.Printf("%s ok (rs %s)\n", node.Name, resp.Version)
	return 0
}
