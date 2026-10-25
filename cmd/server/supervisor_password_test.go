package main

import (
	"os"
	"strings"
	"testing"
)

func TestTakeSupervisorPasswordClearsEnv(t *testing.T) {
	t.Setenv(supervisorPasswordEnv, strings.Repeat("a", 32))
	got, err := takeSupervisorPassword()
	if err != nil || got != strings.Repeat("a", 32) {
		t.Fatalf("got %q, %v", got, err)
	}
	if _, set := os.LookupEnv(supervisorPasswordEnv); set {
		t.Fatal("env var still set; child processes would inherit it")
	}
}

func TestTakeSupervisorPasswordRejectsShort(t *testing.T) {
	t.Setenv(supervisorPasswordEnv, "short")
	if _, err := takeSupervisorPassword(); err == nil {
		t.Fatal("short password accepted")
	}
}
