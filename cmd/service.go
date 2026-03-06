package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	plistLabel = "dev.harato.torii"
	plistPath  = "Library/LaunchAgents/dev.harato.torii.plist"
	logPath    = ".local/share/torii/torii.log"
	unitName   = "torii"
)

func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return home
}

func run(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			os.Exit(exitErr.ExitCode())
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func Start() {
	switch runtime.GOOS {
	case "darwin":
		plist := homeDir() + "/" + plistPath
		run("launchctl", "load", plist)
	case "linux":
		run("systemctl", "--user", "start", unitName)
	default:
		fmt.Fprintf(os.Stderr, "unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func Stop() {
	switch runtime.GOOS {
	case "darwin":
		plist := homeDir() + "/" + plistPath
		run("launchctl", "unload", plist)
	case "linux":
		run("systemctl", "--user", "stop", unitName)
	default:
		fmt.Fprintf(os.Stderr, "unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func Restart() {
	switch runtime.GOOS {
	case "darwin":
		plist := homeDir() + "/" + plistPath
		run("launchctl", "unload", plist)
		run("launchctl", "load", plist)
	case "linux":
		run("systemctl", "--user", "restart", unitName)
	default:
		fmt.Fprintf(os.Stderr, "unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func Status() {
	switch runtime.GOOS {
	case "darwin":
		cmd := exec.Command("launchctl", "list")
		out, err := cmd.Output()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		// Filter for our service
		found := false
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, plistLabel) {
				fmt.Println(line)
				found = true
			}
		}
		if !found {
			fmt.Println("torii service is not loaded")
		}
	case "linux":
		run("systemctl", "--user", "status", unitName)
	default:
		fmt.Fprintf(os.Stderr, "unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func Logs() {
	switch runtime.GOOS {
	case "darwin":
		logFile := homeDir() + "/" + logPath
		run("tail", "-f", logFile)
	case "linux":
		run("journalctl", "--user", "-f", "-u", unitName)
	default:
		fmt.Fprintf(os.Stderr, "unsupported OS: %s\n", runtime.GOOS)
		os.Exit(1)
	}
}

func Usage() {
	fmt.Fprintf(os.Stderr, "Usage: torii [command]\n\n")
	fmt.Fprintf(os.Stderr, "Commands:\n")
	fmt.Fprintf(os.Stderr, "  start     Start the torii service\n")
	fmt.Fprintf(os.Stderr, "  stop      Stop the torii service\n")
	fmt.Fprintf(os.Stderr, "  restart   Restart the torii service\n")
	fmt.Fprintf(os.Stderr, "  status    Show service status\n")
	fmt.Fprintf(os.Stderr, "  logs      Tail service logs\n")
	fmt.Fprintf(os.Stderr, "\nWithout a command, torii starts the bot directly.\n")
	os.Exit(1)
}

