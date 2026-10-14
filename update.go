package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"atlas/rs/internal/fsx"
)

const (
	githubLatestReleaseURL = "https://github.com/xbenduan/rs/releases/latest"
	githubReleaseBaseURL   = "https://github.com/xbenduan/rs/releases/download"
	maxChecksumBytes       = 1 << 20
	maxUpdateBytes         = 200 << 20
)

var updateHTTPClient = &http.Client{
	Timeout: 5 * time.Minute,
	CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		if request.URL.Scheme != "https" || !trustedUpdateHost(request.URL.Hostname()) {
			return errors.New("update redirect left trusted GitHub HTTPS hosts")
		}
		return nil
	},
}

func trustedUpdateHost(host string) bool {
	return host == "github.com" ||
		host == "objects.githubusercontent.com" ||
		strings.HasSuffix(host, ".githubusercontent.com")
}

type releaseVersion struct {
	major int
	minor int
	patch int
}

func parseReleaseVersion(raw string) (releaseVersion, error) {
	raw = strings.TrimPrefix(raw, "v")
	parts := strings.Split(raw, ".")
	if len(parts) != 3 {
		return releaseVersion{}, fmt.Errorf("invalid release version %q", raw)
	}
	values := make([]int, 3)
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 || strconv.Itoa(value) != part {
			return releaseVersion{}, fmt.Errorf("invalid release version %q", raw)
		}
		values[i] = value
	}
	return releaseVersion{major: values[0], minor: values[1], patch: values[2]}, nil
}

func (v releaseVersion) compare(other releaseVersion) int {
	left := [...]int{v.major, v.minor, v.patch}
	right := [...]int{other.major, other.minor, other.patch}
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

func cmdUpdate(args []string) int {
	if len(args) != 0 {
		fmt.Fprintln(os.Stderr, "error: usage: rs update")
		return 1
	}
	if runtime.GOOS == "windows" {
		fmt.Fprintln(os.Stderr, "error: rs update cannot atomically replace a running Windows executable; install the latest Release manually")
		return 1
	}

	executable, err := os.Executable()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: locate current executable: %v\n", err)
		return 1
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: resolve current executable: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	updated, latest, err := updateExecutable(ctx, updateHTTPClient, githubLatestReleaseURL, githubReleaseBaseURL, executable)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: update: %v\n", err)
		return 1
	}
	if !updated {
		if latest == version {
			fmt.Printf("rs %s is already up to date\n", version)
		} else {
			fmt.Printf("rs %s is newer than latest %s; no update applied\n", version, latest)
		}
		return 0
	}
	fmt.Printf("updated rs %s -> %s\n", version, latest)
	return 0
}

func updateExecutable(ctx context.Context, client *http.Client, latestURL, releaseBaseURL, executable string) (bool, string, error) {
	current, err := parseReleaseVersion(version)
	if err != nil {
		return false, "", fmt.Errorf("parse current version: %w", err)
	}
	latest, latestText, err := resolveLatestVersion(ctx, client, latestURL)
	if err != nil {
		return false, "", err
	}
	if latest.compare(current) <= 0 {
		return false, latestText, nil
	}

	asset, err := updateAssetName()
	if err != nil {
		return false, "", err
	}
	releaseURL := strings.TrimRight(releaseBaseURL, "/") + "/v" + latestText
	checksums, err := downloadBytes(ctx, client, releaseURL+"/SHA256SUMS", maxChecksumBytes)
	if err != nil {
		return false, "", fmt.Errorf("download SHA256SUMS: %w", err)
	}
	expected, err := checksumForAsset(checksums, asset)
	if err != nil {
		return false, "", err
	}
	binary, err := downloadBytes(ctx, client, releaseURL+"/"+asset, maxUpdateBytes)
	if err != nil {
		return false, "", fmt.Errorf("download %s: %w", asset, err)
	}
	actual := sha256.Sum256(binary)
	if !strings.EqualFold(hex.EncodeToString(actual[:]), expected) {
		return false, "", fmt.Errorf("SHA-256 mismatch for %s", asset)
	}

	info, err := os.Stat(executable)
	if err != nil {
		return false, "", fmt.Errorf("stat current executable: %w", err)
	}
	if err := fsx.WriteFileAtomic(executable, binary, info.Mode().Perm(), true); err != nil {
		return false, "", fmt.Errorf("replace current executable: %w", err)
	}
	return true, latestText, nil
}

func resolveLatestVersion(ctx context.Context, client *http.Client, latestURL string) (releaseVersion, string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodHead, latestURL, nil)
	if err != nil {
		return releaseVersion{}, "", err
	}
	response, err := client.Do(request)
	if err != nil {
		return releaseVersion{}, "", fmt.Errorf("check latest Release: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return releaseVersion{}, "", fmt.Errorf("check latest Release: HTTP %s", response.Status)
	}
	tag := path.Base(response.Request.URL.Path)
	if !strings.HasPrefix(tag, "v") {
		return releaseVersion{}, "", fmt.Errorf("latest Release URL has invalid tag %q", tag)
	}
	parsed, err := parseReleaseVersion(tag)
	if err != nil {
		return releaseVersion{}, "", err
	}
	return parsed, strings.TrimPrefix(tag, "v"), nil
}

func updateAssetName() (string, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" && runtime.GOOS != "windows" {
		return "", fmt.Errorf("unsupported operating system %s", runtime.GOOS)
	}
	if runtime.GOARCH != "amd64" && runtime.GOARCH != "arm64" {
		return "", fmt.Errorf("unsupported architecture %s", runtime.GOARCH)
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	return fmt.Sprintf("rs-%s-%s%s", runtime.GOOS, runtime.GOARCH, suffix), nil
}

func downloadBytes(ctx context.Context, client *http.Client, rawURL string, limit int64) ([]byte, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, err
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %s", response.Status)
	}
	if response.ContentLength > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("response exceeds %d bytes", limit)
	}
	return data, nil
}

func checksumForAsset(data []byte, asset string) (string, error) {
	var checksum string
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != asset {
			continue
		}
		if checksum != "" {
			return "", fmt.Errorf("SHA256SUMS contains duplicate entry for %s", asset)
		}
		if len(fields[0]) != sha256.Size*2 {
			return "", fmt.Errorf("SHA256SUMS has invalid hash for %s", asset)
		}
		if _, err := hex.DecodeString(fields[0]); err != nil {
			return "", fmt.Errorf("SHA256SUMS has invalid hash for %s", asset)
		}
		checksum = strings.ToLower(fields[0])
	}
	if checksum == "" {
		return "", fmt.Errorf("SHA256SUMS has no entry for %s", asset)
	}
	return checksum, nil
}
