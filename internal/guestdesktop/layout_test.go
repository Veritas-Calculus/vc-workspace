package guestdesktop

import (
	"os/exec"
	"strings"
	"testing"
)

func TestInstallScriptSyntax(t *testing.T) {
	command := exec.Command("sh", "-n")
	command.Stdin = strings.NewReader(InstallScript())
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("install script: %v: %s", err, output)
	}
}

func TestSessionLayout(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal("python3 is required for the Guest display regression gate")
	}
	command := exec.Command(python, "-B", "session_layout_test.py", "-v")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("session layout: %v: %s", err, output)
	}
}
