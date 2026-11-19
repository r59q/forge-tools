package agent_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func scriptedName(prompt string) (string, bool) {
	if name, ok := strings.CutPrefix(prompt, "@fake "); ok {
		return name, true
	}
	if strings.HasPrefix(prompt, "Your child agents have finished.") {
		return "continuation", true
	}
	return "", false
}

// namedScript selects a fake script by prompt instead of workspace, so
// even agents sharing a workspace can execute different scripts in parallel.
func namedScript(name string, prompt []byte) int {
	dir := os.Getenv(scriptsEnv)
	script, err := os.ReadFile(filepath.Join(dir, name+".jsonl"))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	cwd, _ := os.Getwd()
	_ = os.WriteFile(filepath.Join(dir, name+".cwd"), []byte(cwd), 0o600)
	_ = os.WriteFile(filepath.Join(dir, name+".prompt"), prompt, 0o600)
	ids := map[string]string{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(script)), "\n") {
		var d directive
		if json.Unmarshal([]byte(line), &d) != nil || d.Fake == "" {
			fmt.Println(line)
			continue
		}
		switch d.Fake {
		case "mcp":
			toolCall(dir, d, ids)
		case "gate":
			if !fileGate(filepath.Join(dir, d.Text)) {
				fmt.Fprintln(os.Stderr, "gate timed out: "+d.Text)
				return 2
			}
		default:
			if code, done := act(d); done {
				return code
			}
		}
	}
	return 0
}

func toolCall(dir string, d directive, ids map[string]string) {
	args := d.Args
	if args == nil {
		args = make(map[string]any)
	}
	childIDs := make([]string, 0, len(d.Children))
	for _, child := range d.Children {
		id, ok := ids[child]
		if !ok {
			id = child
		}
		childIDs = append(childIDs, id)
	}
	if len(childIDs) == 1 && d.Tool != "wait_agents" {
		args["id"] = childIDs[0]
	} else if len(childIDs) > 0 {
		args["ids"] = childIDs
	}
	result := callTool(d.Tool, args)
	var spawned struct {
		ID string `json:"id"`
	}
	if d.Tool == "spawn_agent" && json.Unmarshal([]byte(result), &spawned) == nil {
		ids[d.Alias] = spawned.ID
	}
	if d.Alias != "" {
		part := filepath.Join(dir, d.Alias+".response")
		_ = os.WriteFile(part+".tmp", []byte(result), 0o600)
		_ = os.Rename(part+".tmp", part)
	}
}

func fileGate(path string) bool {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}
