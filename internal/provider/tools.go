package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const maxToolOutput = 64 * 1024

type reviewTool struct {
	name, description string
	parameters        map[string]any
}

func reviewTools() []reviewTool {
	return []reviewTool{
		{
			name:        "bash",
			description: "Run a Bash command on the local machine to inspect files, search code, use Git, or run tests. Each call starts a fresh shell in the repository root unless workdir is specified; shell state does not persist. Commands have the user's filesystem and network access without approval prompts. Returns combined stdout/stderr, exit_code, timeout/error details, and an explicit truncation flag after 64 KiB. Use focused commands or redirect large output to a file and read it in pages. Do not edit source, commit, publish, or run destructive commands during a review.",
			parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"command", "workdir", "timeout_seconds"},
				"properties": map[string]any{
					"command":         map[string]any{"type": "string", "description": "Bash command to execute."},
					"workdir":         map[string]any{"type": "string", "description": "Empty for repository root; otherwise an absolute path or a path relative to the repository root."},
					"timeout_seconds": map[string]any{"type": "integer", "minimum": 1, "maximum": 120, "description": "Command timeout in seconds, from 1 to 120."},
				},
			},
		},
		{
			name:        "read_file",
			description: "Read a local regular file, including files outside the patch or repository. Relative paths are resolved from the repository root. Returns text from a zero-based byte offset, capped at max_bytes, and a truncation flag if more data remains. Read subsequent pages by increasing offset by the number of bytes requested. Use Bash and git show to inspect committed or staged content when it differs from the working tree.",
			parameters: map[string]any{
				"type": "object", "additionalProperties": false,
				"required": []string{"path", "offset", "max_bytes"},
				"properties": map[string]any{
					"path":      map[string]any{"type": "string"},
					"offset":    map[string]any{"type": "integer", "minimum": 0},
					"max_bytes": map[string]any{"type": "integer", "minimum": 1, "maximum": maxToolOutput},
				},
			},
		},
	}
}

type toolResult struct {
	Output    string `json:"output"`
	ExitCode  *int   `json:"exit_code,omitempty"`
	Truncated bool   `json:"truncated"`
	TimedOut  bool   `json:"timed_out,omitempty"`
	Error     string `json:"error,omitempty"`
}

func (r toolResult) json() string {
	raw, _ := json.Marshal(r)
	return string(raw)
}

func executeTool(ctx context.Context, root, name string, raw json.RawMessage) toolResult {
	if err := ctx.Err(); err != nil {
		return toolResult{Error: err.Error()}
	}
	switch name {
	case "bash":
		var args struct {
			Command        string `json:"command"`
			Workdir        string `json:"workdir"`
			TimeoutSeconds int    `json:"timeout_seconds"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return toolResult{Error: err.Error()}
		}
		if strings.TrimSpace(args.Command) == "" || args.TimeoutSeconds < 1 || args.TimeoutSeconds > 120 {
			return toolResult{Error: "command must be nonempty and timeout_seconds must be between 1 and 120"}
		}
		commandCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
		defer cancel()
		command := exec.CommandContext(commandCtx, "bash", "--noprofile", "--norc", "-c", args.Command)
		command.Dir = toolPath(root, args.Workdir)
		command.WaitDelay = time.Second
		cleanup := configureToolProcess(command)
		defer cleanup()
		var output cappedOutput
		command.Stdout, command.Stderr = &output, &output
		err := command.Run()
		result := toolResult{Output: output.String(), Truncated: output.truncated}
		if command.ProcessState != nil {
			code := command.ProcessState.ExitCode()
			result.ExitCode = &code
		}
		if err != nil {
			result.Error = err.Error()
		}
		if commandCtx.Err() != nil {
			result.Error = commandCtx.Err().Error()
			result.TimedOut = errors.Is(commandCtx.Err(), context.DeadlineExceeded)
		}
		return result
	case "read_file":
		var args struct {
			Path     string `json:"path"`
			Offset   int64  `json:"offset"`
			MaxBytes int    `json:"max_bytes"`
		}
		if err := decodeToolArguments(raw, &args); err != nil {
			return toolResult{Error: err.Error()}
		}
		if args.Path == "" || args.Offset < 0 || args.MaxBytes < 1 || args.MaxBytes > maxToolOutput {
			return toolResult{Error: "path must be nonempty, offset nonnegative, and max_bytes between 1 and 65536"}
		}
		path := toolPath(root, args.Path)
		info, err := os.Stat(path)
		if err != nil {
			return toolResult{Error: err.Error()}
		}
		if !info.Mode().IsRegular() {
			return toolResult{Error: "read_file requires a regular file"}
		}
		file, err := os.Open(path)
		if err != nil {
			return toolResult{Error: err.Error()}
		}
		defer file.Close()
		data := make([]byte, args.MaxBytes+1)
		n, err := file.ReadAt(data, args.Offset)
		if err != nil && err != io.EOF {
			return toolResult{Error: err.Error()}
		}
		truncated := n > args.MaxBytes
		if truncated {
			n = args.MaxBytes
		}
		return toolResult{Output: string(data[:n]), Truncated: truncated}
	default:
		return toolResult{Error: fmt.Sprintf("unknown tool %q; use bash or read_file", name)}
	}
}

func toolPath(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(root, path)
}

func decodeToolArguments(raw json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("invalid tool arguments: %w", err)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid tool arguments: trailing data")
	}
	return nil
}

// Keep draining stdout/stderr so a verbose command cannot block on a full pipe.
// os/exec serializes writes because both streams share the same writer.
type cappedOutput struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *cappedOutput) String() string { return b.buffer.String() }

func (b *cappedOutput) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxToolOutput - b.buffer.Len()
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}
