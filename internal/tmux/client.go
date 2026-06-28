package tmux

import (
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

func HasSession(name string) bool {
	err := exec.Command("tmux", "has-session", "-t", name).Run()
	return err == nil
}

func NewSession(name, dir string) error {
	return exec.Command("tmux", "new-session", "-d", "-s", name, "-c", dir).Run()
}

func RenameWindow(session string, index int, newName string) error {
	target := sessionWindow(session, index)
	return exec.Command("tmux", "rename-window", "-t", target, newName).Run()
}

func NewWindow(session, name, dir string) error {
	return exec.Command("tmux", "new-window", "-t", session, "-n", name, "-c", dir).Run()
}

func SendKeys(session string, index int, keys string) error {
	target := sessionWindow(session, index)
	return exec.Command("tmux", "send-keys", "-t", target, keys, "Enter").Run()
}

func SelectWindow(session string, index int) error {
	target := sessionWindow(session, index)
	return exec.Command("tmux", "select-window", "-t", target).Run()
}

func SwitchClient(session string) error {
	return exec.Command("tmux", "switch-client", "-t", session).Run()
}

func AttachSession(session string) error {
	cmd := exec.Command("tmux", "attach-session", "-t", session)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func ListSessions() ([]string, error) {
	out, err := exec.Command("tmux", "list-sessions", "-F", "#{session_name}").Output()
	if err != nil {
		return nil, err
	}
	var sessions []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			sessions = append(sessions, line)
		}
	}
	return sessions, nil
}

func sessionWindow(session string, index int) string {
	return session + ":" + strconv.Itoa(index)
}
