package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func main() {
	app := "Google Chrome"
	if len(os.Args) > 1 && os.Args[1] == "dev" {
		app = "Google Chrome Dev"
	}

	script := `
tell application "` + app + `"
	set windowList to every window
	repeat with aWindow in windowList
		set tabList to every tab of aWindow
		repeat with aTab in tabList
			if URL of aTab contains "test-okone.okg.com" then
				return execute aTab javascript "JSON.parse(localStorage.getItem('ok_okone')).token"
			end if
		end repeat
	end repeat
end tell
return "NO_OKONE_TAB"
`

	out, err := exec.Command("osascript", "-e", script).CombinedOutput()
	if err != nil {
		fmt.Fprintf(os.Stderr, "osascript failed: %v\n%s\n", err, out)
		os.Exit(1)
	}
	token := strings.TrimSpace(string(out))
	if token == "NO_OKONE_TAB" {
		fmt.Fprintf(os.Stderr, "No OKOne tab found in %s. Please open test-okone.okg.com first.\n", app)
		os.Exit(1)
	}
	fmt.Print(token)
}
