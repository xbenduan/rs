package main

import "testing"

func TestExtractIntFlagRejectsMissingAndDuplicateValues(t *testing.T) {
	tests := [][]string{
		{"node", "--timeout"},
		{"node", "--timeout", "1", "--timeout=2"},
	}
	for _, args := range tests {
		if _, _, err := extractIntFlag(args, "timeout", 30); err == nil {
			t.Fatalf("accepted invalid args: %v", args)
		}
	}
}

func TestCommandsRejectInvalidTimeoutAndExtraArguments(t *testing.T) {
	if code := cmdShell([]string{"prod", "true", "--timeout=-1"}); code == 0 {
		t.Fatal("shell accepted a negative timeout")
	}
	if code := cmdShell([]string{"prod", "true", "--timeout=2147483648"}); code == 0 {
		t.Fatal("shell accepted a timeout that overflows int32")
	}
	if code := cmdPush([]string{"prod", "local", "/remote", "extra"}); code == 0 {
		t.Fatal("push accepted an extra argument")
	}
	if code := cmdHealth([]string{"prod", "extra"}); code == 0 {
		t.Fatal("health accepted an extra argument")
	}
	if code := cmdNode([]string{"rm", "prod", "extra"}); code == 0 {
		t.Fatal("node rm accepted an extra argument")
	}
}

func TestVersionsCompatible(t *testing.T) {
	if !versionsCompatible("1.2.0", "1.0.0") {
		t.Fatal("same major versions were rejected")
	}
	for _, serverVersion := range []string{"2.0.0", "", "invalid"} {
		if versionsCompatible("1.2.0", serverVersion) {
			t.Fatalf("incompatible server version %q was accepted", serverVersion)
		}
	}
}
