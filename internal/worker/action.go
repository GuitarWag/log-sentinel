package worker

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"text/template"
	"time"

	"github.com/log-sentinel/sentinel/internal/config"
)

type Action interface {
	Run(ctx context.Context, ticket *Ticket) error
	Name() string
}

func BuildAction(cfg config.ActionConfig) (Action, error) {
	timeout := cfg.Timeout.Duration
	if timeout == 0 {
		timeout = 60 * time.Second
	}

	switch cfg.Type {
	case "webhook":
		if cfg.URL == "" {
			return nil, fmt.Errorf("webhook action requires url")
		}
		return &WebhookAction{cfg: cfg, timeout: timeout}, nil

	case "cli_agent":
		if cfg.Command == "" {
			return nil, fmt.Errorf("cli_agent action requires command")
		}
		mode := cfg.PromptMode
		if mode == "" {
			mode = "arg"
		}
		if mode != "arg" && mode != "stdin" {
			return nil, fmt.Errorf("cli_agent prompt_mode must be 'arg' or 'stdin', got %q", mode)
		}
		return &CLIAgentAction{cfg: cfg, timeout: timeout, promptMode: mode}, nil

	default:
		return nil, fmt.Errorf("unknown action type %q (valid: webhook, cli_agent)", cfg.Type)
	}
}

type WebhookAction struct {
	cfg     config.ActionConfig
	timeout time.Duration
}

func (a *WebhookAction) Name() string {
	if a.cfg.Name != "" {
		return a.cfg.Name
	}
	return "webhook:" + a.cfg.URL
}

func (a *WebhookAction) Run(ctx context.Context, ticket *Ticket) error {
	body, err := json.Marshal(ticket)
	if err != nil {
		return fmt.Errorf("marshaling ticket: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	for k, v := range a.cfg.Headers {
		req.Header.Set(k, v)
	}

	if a.cfg.Secret != "" {
		mac := hmac.New(sha256.New, []byte(a.cfg.Secret))
		mac.Write(body)
		req.Header.Set("X-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("POST %s: %w", a.cfg.URL, err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned HTTP %d", resp.StatusCode)
	}

	slog.Info("webhook delivered", "action", a.Name(), "status", resp.StatusCode)
	return nil
}

const defaultPromptTemplate = `You are a software engineer investigating a production error.

Application: {{.AppName}}
Component:   {{.Component}}
Severity:    {{.Severity}}
Error type:  {{.Classification}}
First seen:  {{.FirstSeen}}

Error fingerprint:
{{.FingerprintText}}

Raw log line:
{{.RawLog}}

Investigate the root cause of this error, propose a fix, and if you have access to the codebase, attempt to implement and test it.`

type CLIAgentAction struct {
	cfg        config.ActionConfig
	timeout    time.Duration
	promptMode string
}

func (a *CLIAgentAction) Name() string {
	if a.cfg.Name != "" {
		return a.cfg.Name
	}
	return "cli_agent:" + a.cfg.Command
}

func (a *CLIAgentAction) Run(ctx context.Context, ticket *Ticket) error {
	prompt, err := a.renderPrompt(ticket)
	if err != nil {
		return fmt.Errorf("rendering prompt: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()

	args := make([]string, len(a.cfg.Args))
	copy(args, a.cfg.Args)

	var stdin io.Reader
	if a.promptMode == "arg" {
		args = append(args, prompt)
	} else {
		stdin = strings.NewReader(prompt)
	}

	cmd := exec.CommandContext(ctx, a.cfg.Command, args...)

	if a.cfg.WorkingDir != "" {
		cmd.Dir = a.cfg.WorkingDir
	}

	env := os.Environ()
	for k, v := range a.cfg.Env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env

	if stdin != nil {
		cmd.Stdin = stdin
	}

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	slog.Info("spawning cli agent",
		"action", a.Name(),
		"command", a.cfg.Command,
		"args_count", len(args),
		"working_dir", a.cfg.WorkingDir,
		"prompt_mode", a.promptMode,
	)

	if err := cmd.Run(); err != nil {
		output := truncate(out.String(), 500)
		return fmt.Errorf("cli agent exited with error: %w\noutput: %s", err, output)
	}

	slog.Info("cli agent completed", "action", a.Name(), "output_bytes", out.Len())
	return nil
}

func (a *CLIAgentAction) renderPrompt(ticket *Ticket) (string, error) {
	tmplStr := a.cfg.PromptTemplate
	if tmplStr == "" {
		tmplStr = defaultPromptTemplate
	}

	tmpl, err := template.New("prompt").Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("parsing prompt template: %w", err)
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, ticket); err != nil {
		return "", fmt.Errorf("executing prompt template: %w", err)
	}

	return buf.String(), nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
