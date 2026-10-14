// Package main — rs CLI. Manages the local node list (~/.rs/node.json) and
// drives the rs gRPC service (shell, upload, health).
package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"atlas/rs/internal/fsx"
	"atlas/rs/internal/protocol"
)

// Node is one entry in the local node list and the credential claim protocol.
type Node = protocol.ClaimNode

type nodeListEntry struct {
	Name string `json:"name"`
	ID   string `json:"id,omitempty"`
	IP   string `json:"ip,omitempty"`
	URL  string `json:"url"`
}

// nodeFile returns the path of the local node list, overridable for tests.
func nodeFile() string {
	if v := os.Getenv("RS_NODE_FILE"); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".", ".rs", "node.json")
	}
	return filepath.Join(home, ".rs", "node.json")
}

var errNodeNotFound = errors.New("node not found")

const maxNodeJSONSize = 1 << 20

// loadNodes reads the node list. A missing file is an empty list.
func loadNodes() ([]Node, error) {
	unlock, err := lockNodes()
	if err != nil {
		return nil, err
	}
	defer unlock()
	return loadNodesUnlocked()
}

func lockNodes() (func() error, error) {
	return fsx.LockFile(nodeFile() + ".lock")
}

func loadNodesUnlocked() ([]Node, error) {
	path := nodeFile()
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(data) == 0) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var nodes []Node
	if err := json.Unmarshal(data, &nodes); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	for i := range nodes {
		password, err := unprotectNodePassword(nodes[i].Password)
		if err != nil {
			return nil, fmt.Errorf("decrypt password for node %q: %w", nodes[i].Name, err)
		}
		nodes[i].Password = password
	}
	return nodes, nil
}

// decodeNodeJSON reads one strict, non-empty JSON array from r.
func decodeNodeJSON(r io.Reader) ([]Node, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxNodeJSONSize+1))
	if err != nil {
		return nil, fmt.Errorf("read node JSON: %w", err)
	}
	if len(data) > maxNodeJSONSize {
		return nil, fmt.Errorf("node JSON exceeds %d bytes", maxNodeJSONSize)
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var nodes []Node
	if err := decoder.Decode(&nodes); err != nil {
		return nil, fmt.Errorf("parse node JSON: %w", err)
	}
	if len(nodes) == 0 {
		return nil, errors.New("node JSON must contain at least one node")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, errors.New("node JSON must contain exactly one array")
	}
	for i := range nodes {
		nodes[i].URL = strings.TrimRight(nodes[i].URL, "/")
		if err := validateNode(nodes[i]); err != nil {
			return nil, fmt.Errorf("node %d: %w", i, err)
		}
	}
	return nodes, checkUniqueNodes(nodes)
}

// saveNodes writes the node list atomically with mode 0600.
func saveNodes(nodes []Node) error {
	stored := append([]Node(nil), nodes...)
	for i := range stored {
		password, err := protectNodePassword(stored[i].Password)
		if err != nil {
			return fmt.Errorf("encrypt password for node %q: %w", stored[i].Name, err)
		}
		stored[i].Password = password
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}
	return fsx.WriteFileAtomic(nodeFile(), append(data, '\n'), 0o600, true)
}

// upsertNodes atomically inserts nodes, replacing entries with the same name
// or the same server-issued node ID.
func upsertNodes(updates ...Node) error {
	for _, node := range updates {
		if err := validateNode(node); err != nil {
			return err
		}
	}
	if err := checkUniqueNodes(updates); err != nil {
		return err
	}

	unlock, err := lockNodes()
	if err != nil {
		return err
	}
	defer unlock()

	nodes, err := loadNodesUnlocked()
	if err != nil {
		return err
	}
	for _, node := range updates {
		next := make([]Node, 0, len(nodes)+1)
		replaced := false
		for _, existing := range nodes {
			if existing.Name == node.Name || (node.ID != "" && existing.ID == node.ID) {
				if !replaced {
					next = append(next, node)
					replaced = true
				}
				continue
			}
			next = append(next, existing)
		}
		if !replaced {
			next = append(next, node)
		}
		nodes = next
	}
	return saveNodes(nodes)
}

func checkUniqueNodes(nodes []Node) error {
	seenNames := make(map[string]struct{}, len(nodes))
	seenIDs := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if _, exists := seenNames[node.Name]; exists {
			return fmt.Errorf("duplicate node name %q", node.Name)
		}
		seenNames[node.Name] = struct{}{}
		if node.ID != "" {
			if _, exists := seenIDs[node.ID]; exists {
				return fmt.Errorf("duplicate node ID %q", node.ID)
			}
			seenIDs[node.ID] = struct{}{}
		}
	}
	return nil
}

func validateNode(node Node) error {
	if err := protocol.ValidateNodeName(node.Name); err != nil {
		return err
	}
	u, err := url.Parse(node.URL)
	if err != nil || u.Host == "" || u.User != nil {
		return errors.New("invalid account URL")
	}
	if u.Scheme != "https" && (u.Scheme != "http" || !isLoopbackHost(u.Hostname())) {
		return errors.New("account URL must use https (http is allowed only for loopback)")
	}
	if u.RawQuery != "" || u.Fragment != "" {
		return errors.New("account URL must not contain query or fragment")
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "r" || len(parts[1]) < 20 {
		return errors.New("account URL must match https://host/r/<account-id>")
	}
	if len(node.Password) < 12 {
		return errors.New("password must contain at least 12 bytes")
	}
	if node.IP != "" && net.ParseIP(node.IP) == nil {
		return errors.New("node IP is invalid")
	}
	if node.ID != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(node.ID)
		if err != nil || len(decoded) != 16 {
			return errors.New("node ID is invalid")
		}
	}
	return nil
}

// isLoopbackHost reports whether a bare host name or IP stays on this machine.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// removeNode deletes a node by name.
func removeNode(name string) error {
	unlock, err := lockNodes()
	if err != nil {
		return err
	}
	defer unlock()

	nodes, err := loadNodesUnlocked()
	if err != nil {
		return err
	}
	found := false
	kept := nodes[:0]
	for _, n := range nodes {
		if n.Name == name {
			found = true
			continue
		}
		kept = append(kept, n)
	}
	if !found {
		return fmt.Errorf("%w: %s (see `rs node list`)", errNodeNotFound, name)
	}
	return saveNodes(kept)
}

// findNode returns the named node.
func findNode(name string) (Node, error) {
	nodes, err := loadNodes()
	if err != nil {
		return Node{}, err
	}
	for _, n := range nodes {
		if n.Name == name {
			return n, nil
		}
	}
	return Node{}, fmt.Errorf("%w: %s (see `rs node list`)", errNodeNotFound, name)
}
