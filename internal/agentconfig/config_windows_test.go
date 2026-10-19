package agentconfig

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"syscall"
	"testing"
)

func TestHelperCommandProcess(t *testing.T) {
	if os.Getenv("COPILOT_CHAT_TEST_HELPER_PROCESS") != "1" {
		return
	}
	start := slices.Index(os.Args, "--")
	if start == -1 {
		os.Exit(38)
	}
	if err := json.NewEncoder(os.Stdout).Encode(os.Args[start+1:]); err != nil {
		os.Exit(39)
	}
	os.Exit(37)
}

func TestHelperWindowsShellQuotingRoundTrip(t *testing.T) {
	// Use an actual executable and Claude Code's cmd /d /s /c invocation,
	// with no PowerShell or extra compiler dependency.
	dir := filepath.Join(t.TempDir(), "space '$ & (parentheses) ^ 你好")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(dir, "helper.exe")
	if err := os.WriteFile(helper, data, 0700); err != nil {
		t.Fatal(err)
	}
	args := []string{"--provider", "p'$ & () ^", "--data-dir", dir, `C:\`, `C:\trailing slash\`, "", "a&b|c<d>e"}
	command, err := shellCommand("windows", helper, append([]string{"-test.run=^TestHelperCommandProcess$", "--"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("cmd.exe")
	// cmd.exe does not use the native argv quoting that exec.Command defaults to.
	cmd.SysProcAttr = &syscall.SysProcAttr{CmdLine: `cmd.exe /d /s /c "` + command + `"`, HideWindow: true}
	cmd.Env = append(os.Environ(), "COPILOT_CHAT_TEST_HELPER_PROCESS=1")
	output, err := cmd.Output()
	if err == nil || cmd.ProcessState == nil || cmd.ProcessState.ExitCode() != 37 {
		t.Fatalf("exit status lost: %v; output: %s", err, output)
	}
	var got []string
	if err := json.Unmarshal(output, &got); err != nil {
		t.Fatalf("invalid result: %v; output: %s", err, output)
	}
	if !slices.Equal(got, args) {
		t.Fatalf("arguments changed: got %q, want %q", got, args)
	}
}
