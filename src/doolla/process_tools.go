package doolla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// WorkspaceProcess is implemented by the host application's workspace runner.
// The host selects the interpreter, working directory and isolation boundary.
type WorkspaceProcess func(context.Context, User, string, []string) (string, error)

// CommandResult keeps the interpreter's output channels and exit status separate.
type CommandResult struct {
	Stdout, Stderr string
	ExitCode       int
}

type WorkspaceCommand func(context.Context, User, string, []string) (CommandResult, error)

// PythonTools adapts hosts that return combined output.
func PythonTools(run WorkspaceProcess) []Registration {
	if run == nil {
		return nil
	}
	return pythonTools(func(ctx context.Context, u User, program string, args []string) (CommandResult, error) {
		output, err := run(ctx, u, program, args)
		return CommandResult{Stdout: output}, err
	}, true)
}

// PythonToolsWithRunner accepts a command runner with separate stdout and stderr.
func PythonToolsWithRunner(run WorkspaceCommand) []Registration {
	if run == nil {
		return nil
	}
	return pythonTools(run, false)
}

func pythonTools(run WorkspaceCommand, combined bool) []Registration {
	array := map[string]any{"type": "array", "items": map[string]string{"type": "string"}}
	packages := map[string]any{"type": "array", "items": map[string]string{"type": "string"}, "minItems": 1}
	limit := map[string]any{"type": []string{"number", "null"}, "minimum": 0}
	var tools []Registration
	for _, spec := range []ToolSpec{
		{"run_python_file", "Execute a Python file in the user's workspace.", objectSchema(map[string]any{"filename": strField("Workspace file"), "arguments": array, "timeout_seconds": limit}, "filename")},
		{"pip_install", "Install packages into the workspace Python environment.", objectSchema(map[string]any{"packages": packages, "upgrade": map[string]string{"type": "boolean"}, "extra_args": array, "timeout_seconds": limit}, "packages")},
	} {
		tool := functionTool{spec, func(ctx context.Context, u User, raw json.RawMessage) (ToolResult, error) {
			var a struct {
				Filename            string
				Arguments, Packages []string
				Upgrade             bool
				ExtraArgs           []string `json:"extra_args"`
				Timeout             float64  `json:"timeout_seconds"`
			}
			if err := json.Unmarshal(raw, &a); err != nil {
				return ToolResult{}, err
			}
			if a.Timeout < 0 {
				return ToolResult{}, errors.New("negative timeout")
			}
			if a.Timeout > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, time.Duration(a.Timeout*float64(time.Second)))
				defer cancel()
			}
			var args []string
			if spec.Name == "run_python_file" {
				if a.Filename == "" {
					return ToolResult{}, errors.New("filename required")
				}
				args = append([]string{"--", a.Filename}, a.Arguments...)
			} else {
				if len(a.Packages) == 0 {
					return ToolResult{}, errors.New("packages required")
				}
				args = []string{"-m", "pip", "install"}
				if a.Upgrade {
					args = append(args, "--upgrade")
				}
				args = append(args, a.Packages...)
				args = append(args, a.ExtraArgs...)
			}
			output, err := run(ctx, u, "python", args)
			stdout := truncateProcessOutput(output.Stdout)
			if err != nil {
				return ToolResult{}, fmt.Errorf("workspace process: %w: %s", err, stdout)
			}
			parts := []string{"$ " + displayCommand("python", args)}
			if trimmed := strings.TrimSpace(stdout); trimmed != "" {
				parts = append(parts, "STDOUT:\n"+trimmed)
			}
			if stderr := strings.TrimSpace(truncateProcessOutput(output.Stderr)); stderr != "" {
				parts = append(parts, "STDERR:\n"+stderr)
			}
			if len(parts) == 1 {
				parts = append(parts, "(no output)")
			}
			body := strings.Join(parts, "\n\n")
			summary := fmt.Sprintf("Executed python %s (exit code %d).", a.Filename, output.ExitCode)
			if spec.Name == "pip_install" {
				summary = "pip install completed successfully."
				if output.ExitCode != 0 {
					summary = fmt.Sprintf("pip install failed (exit code %d).", output.ExitCode)
				}
			}
			status := "success"
			if output.ExitCode != 0 {
				status = "error"
			}
			result := ToolResult{Content: summary + "\n\n" + body, Component: &Component{Type: "card", Data: map[string]any{"title": "Command Result", "content": body, "status": status}, Simple: plainToolText(summary)}}
			if output.ExitCode != 0 {
				return result, errors.New(result.Content)
			}
			if combined {
				result.Content = stdout
			}
			return result, nil
		}}
		tools = append(tools, Registration{Tool: tool, Groups: []string{"admin"}})
	}
	return tools
}

const maxCommandOutputRunes = 4000

func truncateProcessOutput(text string) string {
	cut, count := 0, 0
	for i := range text {
		count++
		if count == maxCommandOutputRunes {
			cut = i
		}
		if count > maxCommandOutputRunes {
			return text[:cut] + "…"
		}
	}
	return text
}

// Retain enough bytes to distinguish exactly 4000 Unicode characters from
// longer output, while draining both pipes until the process exits.
type processOutput struct {
	buffer bytes.Buffer
}

func (out *processOutput) Write(p []byte) (int, error) {
	remaining := maxCommandOutputRunes*utf8.UTFMax + 1 - out.buffer.Len()
	out.buffer.Write(p[:min(len(p), remaining)])
	return len(p), nil
}

func displayCommand(program string, args []string) string {
	parts := []string{program}
	for _, arg := range args {
		parts = append(parts, strconv.Quote(arg))
	}
	return strings.Join(parts, " ")
}

// LocalPythonProcess executes an explicitly selected interpreter and returns
// its combined output, preserving the WorkspaceProcess interface.
func LocalPythonProcess(files Artifacts, interpreter string) WorkspaceProcess {
	return func(ctx context.Context, u User, program string, args []string) (string, error) {
		cmd, err := workspacePythonCommand(ctx, files, interpreter, u, program, args)
		if err != nil {
			return "", err
		}
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
}

// LocalPythonRunner reports each output channel and the process exit status.
// The embedding application is responsible for process isolation.
func LocalPythonRunner(files Artifacts, interpreter string) WorkspaceCommand {
	return func(ctx context.Context, u User, program string, args []string) (CommandResult, error) {
		cmd, err := workspacePythonCommand(ctx, files, interpreter, u, program, args)
		if err != nil {
			return CommandResult{}, err
		}
		var stdout, stderr processOutput
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err = cmd.Run()
		if ctx.Err() != nil {
			return CommandResult{}, ctx.Err()
		}
		// 非零退出属于脚本执行结果，保留输出和退出码交给工具显示；
		// 命令无法启动或上下文已取消时，调用方收到 Go 错误。
		var exit *exec.ExitError
		if err != nil && !errors.As(err, &exit) {
			return CommandResult{}, err
		}
		return CommandResult{Stdout: truncateProcessOutput(stdout.buffer.String()), Stderr: truncateProcessOutput(stderr.buffer.String()), ExitCode: cmd.ProcessState.ExitCode()}, nil
	}
}

func workspacePythonCommand(ctx context.Context, files Artifacts, interpreter string, u User, program string, args []string) (*exec.Cmd, error) {
	if program != "python" || interpreter == "" {
		return nil, errors.New("workspace Python interpreter is required")
	}
	root, err := files.root(u)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if len(args) >= 2 && args[0] == "--" {
		if _, err = root.Stat(args[1]); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, interpreter, args...)
	cmd.Dir = root.Name()
	return cmd, nil
}
