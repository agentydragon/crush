# Crush

<p align="center">
    <a href="https://stuff.charm.sh/crush/charm-crush.png"><img width="450" alt="Charm Crush Logo" src="https://github.com/user-attachments/assets/adc1a6f4-b284-4603-836c-59038caa2e8b" /></a><br />
    <a href="https://github.com/charmbracelet/crush/releases"><img src="https://img.shields.io/github/release/charmbracelet/crush" alt="Latest Release"></a>
    <a href="https://github.com/charmbracelet/crush/actions"><img src="https://github.com/charmbracelet/crush/workflows/build/badge.svg" alt="Build Status"></a>
</p>

<p align="center">Your new coding bestie, now available in your favourite terminal.<br />Your tools, your code, and your workflows, wired into your LLM of choice.</p>

<p align="center"><img width="800" alt="Crush Demo" src="https://github.com/user-attachments/assets/58280caf-851b-470a-b6f7-d5c4ea8a1968" /></p>

## Features

- **Multi-Model:** choose from a wide range of LLMs or add your own via OpenAI- or Anthropic-compatible APIs
- **Flexible:** switch LLMs mid-session while preserving context
- **Session-Based:** maintain multiple work sessions and contexts per project
- **LSP-Enhanced:** Crush uses LSPs for additional context, just like you do
- **Inline word diffs (optional):** highlight changed words within lines using Git’s `--word-diff=porcelain` (enable via config)
- **Extensible:** add capabilities via MCPs (`http`, `stdio`, and `sse`)
- **Works Everywhere:** first-class support in every terminal on macOS, Linux, Windows (PowerShell and WSL), FreeBSD, OpenBSD, and NetBSD

## Installation

Use a package manager:

```bash
# Homebrew
brew install charmbracelet/tap/crush

# NPM
npm install -g @charmland/crush

# Arch Linux (btw)
yay -S crush-bin

# Nix
nix run github:numtide/nix-ai-tools#crush
```

Windows users:

```bash
# Winget
winget install charmbracelet.crush

# Scoop
scoop bucket add charm https://github.com/charmbracelet/scoop-bucket.git
scoop install crush
```

<details>
<summary><strong>Nix (NUR)</strong></summary>

Crush is available via [NUR](https://github.com/nix-community/NUR) in `nur.repos.charmbracelet.crush`.

You can also try out Crush via `nix-shell`:

```bash
# Add the NUR channel.
nix-channel --add https://github.com/nix-community/NUR/archive/main.tar.gz nur
nix-channel --update

# Get Crush in a Nix shell.
nix-shell -p '(import <nur> { pkgs = import <nixpkgs> {}; }).repos.charmbracelet.crush'
```

</details>

<details>
<summary><strong>Debian/Ubuntu</strong></summary>

```bash
sudo mkdir -p /etc/apt/keyrings
curl -fsSL https://repo.charm.sh/apt/gpg.key | sudo gpg --dearmor -o /etc/apt/keyrings/charm.gpg
echo "deb [signed-by=/etc/apt/keyrings/charm.gpg] https://repo.charm.sh/apt/ * *" | sudo tee /etc/apt/sources.list.d/charm.list
sudo apt update && sudo apt install crush
```

</details>

<details>
<summary><strong>Fedora/RHEL</strong></summary>

```bash
echo '[charm]
name=Charm
baseurl=https://repo.charm.sh/yum/
enabled=1
gpgcheck=1
gpgkey=https://repo.charm.sh/yum/gpg.key' | sudo tee /etc/yum.repos.d/charm.repo
sudo yum install crush
```

</details>

Or, download it:

- [Packages][releases] are available in Debian and RPM formats
- [Binaries][releases] are available for Linux, macOS, Windows, FreeBSD, OpenBSD, and NetBSD

[releases]: https://github.com/charmbracelet/crush/releases

Or just install it with Go:

```
go install github.com/charmbracelet/crush@latest
```

> [!WARNING]
> Productivity may increase when using Crush and you may find yourself nerd
> sniped when first using the application. If the symptoms persist, join the
> [Discord][discord] and nerd snipe the rest of us.

## Getting Started

The quickest way to get started is to grab an API key for your preferred
provider such as Anthropic, OpenAI, Groq, or OpenRouter and just start
Crush. You'll be prompted to enter your API key.

That said, you can also set environment variables for preferred providers.

| Environment Variable       | Provider                                           |
| -------------------------- | -------------------------------------------------- |
| `ANTHROPIC_API_KEY`        | Anthropic                                          |
| `OPENAI_API_KEY`           | OpenAI                                             |
| `OPENROUTER_API_KEY`       | OpenRouter                                         |
| `GEMINI_API_KEY`           | Google Gemini                                      |
| `VERTEXAI_PROJECT`         | Google Cloud VertexAI (Gemini)                     |
| `VERTEXAI_LOCATION`        | Google Cloud VertexAI (Gemini)                     |
| `GROQ_API_KEY`             | Groq                                               |
| `AWS_ACCESS_KEY_ID`        | AWS Bedrock (Claude)                               |
| `AWS_SECRET_ACCESS_KEY`    | AWS Bedrock (Claude)                               |
| `AWS_REGION`               | AWS Bedrock (Claude)                               |
| `AZURE_OPENAI_ENDPOINT`    | Azure OpenAI models                                |
| `AZURE_OPENAI_API_KEY`     | Azure OpenAI models (optional when using Entra ID) |
| `AZURE_OPENAI_API_VERSION` | Azure OpenAI models                                |

### By the Way

Is there a provider you’d like to see in Crush? Is there an existing model that needs an update?

Crush’s default model listing is managed in [Catwalk](https://github.com/charmbracelet/catwalk), an community-supported, open source repository of Crush-compatible models, and you’re welcome to contribute.

<a href="https://github.com/charmbracelet/catwalk"><img width="174" height="174" alt="Catwalk Badge" src="https://github.com/user-attachments/assets/95b49515-fe82-4409-b10d-5beb0873787d" /></a>

## Configuration

Crush runs great with no configuration. That said, if you do need or want to
customize Crush, configuration can be added either local to the project itself,
or globally, with the following priority:

1. `.crush.json`
2. `crush.json`
3. `$HOME/.config/crush/crush.json` (Windows: `%USERPROFILE%\AppData\Local\crush\crush.json`)

Configuration itself is stored as a JSON object:

```json
{
   "this-setting": {"this": "that"},
   "that-setting": ["ceci", "cela"]
}
```

As an additional note, Crush also stores ephemeral data, such as application state, in one additional location:

```bash
# Unix
$HOME/.local/share/crush/crush.json

# Windows
%LOCALAPPDATA%\crush\crush.json
```

### LSPs

#### LSP file watching modes

Crush supports three LSP watch modes via `options.lsp_watch_mode` in `crush.json`:

- `on_demand` (recommended for huge repos):
  - No recursive OS watchers are installed.
  - Only files the agent opens/touches are sent to the LSP (`didOpen/didChange/didClose`).
  - Rely on the LSP’s own filesystem watching for broader indexing.

- `limited` (default):
  - Installs a recursive watcher with a hard cap on watched directories and EMFILE guards.
  - Forwards `workspace/didChangeWatchedFiles` to the LSP, and does a small, bounded preload of high‑priority files for better initial diagnostics.
  - Safer for large repos: stops recursing when cap is hit and logs a warning.

- `recursive` (advanced, risky on huge repos):
  - Aggressive recursive watching with a very high cap.
  - Can exhaust file descriptors (EMFILE). Prefer `limited` or `on_demand`.

Environment override:

- Set `CRUSH_MAX_WATCHED_DIRS` to adjust the cap in `limited` mode.

Notes:

- We forward changes to the LSP (via LSP notifications) so the server can re‑analyze files on disk changes originating outside of Crush.
- TODO: Add ignore patterns in config for the recursive watcher (today we filter a set of common build dirs: `.git`, `node_modules`, `dist`, `target`, `vendor`, etc.).
- TODO: Implement macOS FSEvents backend to reduce FD usage vs kqueue per directory.

#### Ignore rules (.crushignore and ignore_globs)

- Search tools (grep, glob) respect both `.gitignore` and `.crushignore` at the workspace root automatically.
- The recursive LSP watcher respects:
  - `.crushignore` at the workspace root (gitignore semantics)
  - Optional, per‑LSP `ignore_globs` (doublestar patterns matched against workspace‑relative paths)
- TODO: Add `.gitignore` support to the recursive watcher as well (today only the search tools consume `.gitignore`).

Example `ignore_globs` in LSP config:

```json
{
  "$schema": "https://charm.land/crush.json",
  "lsp": {
    "typescript": {
      "command": "typescript-language-server",
      "args": ["--stdio"],
      "watch_mode": "recursive",
      "recursive_max_watched_dirs": 4000,
      "ignore_globs": [
        "**/bazel-out/**",
        "terraform/.terraform/**"
      ]
    }
  }
}
```

#### Example

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "lsp_watch_mode": "on_demand"
  }
}
```

Crush can use LSPs for additional context to help inform its decisions, just
like you would. LSPs can be added manually like so:

```json
{
  "$schema": "https://charm.land/crush.json",
  "lsp": {
    "go": {
      "command": "gopls"
    },
    "typescript": {
      "command": "typescript-language-server",
      "args": ["--stdio"]
    },
    "nix": {
      "command": "nil"
    }
  }
}
```

### MCPs

Crush also supports Model Context Protocol (MCP) servers through three
transport types: `stdio` for command-line servers, `http` for HTTP endpoints,
and `sse` for Server-Sent Events. Environment variable expansion is supported
using `$(echo $VAR)` syntax.

Runtime environment for MCP servers
- stdio: launched as a child process of Crush in the current working directory (or the directory provided via `--cwd`).
  - Environment: starts with the OS environment (including variables loaded from `.env`) and appends `mcp.<name>.env` after resolving `$VARS` and `$(command)`.
  - Args: taken from `mcp.<name>.args`.
  - Startup timeout: controlled by `options.mcp.init_timeout_secs` (default 10s) covering connect+initialize; tool calls use `options.mcp.tool_timeout_secs` (default 120s).
- http/sse: Crush connects to `mcp.<name>.url` and attaches resolved `mcp.<name>.headers`. No local process is spawned.

```json
{
  "$schema": "https://charm.land/crush.json",
  "mcp": {
    "filesystem": {
      "type": "stdio",
      "command": "node",
      "args": ["/path/to/mcp-server.js"],
      "env": {
        "NODE_ENV": "production"
      }
    },
    "github": {
      "type": "http",
      "url": "https://example.com/mcp/",
      "headers": {
        "Authorization": "$(echo Bearer $EXAMPLE_MCP_TOKEN)"
      }
    },
    "streaming-service": {
      "type": "sse",
      "url": "https://example.com/mcp/sse",
      "headers": {
        "API-Key": "$(echo $API_KEY)"
      }
    }
  }
}
```

### Diff Engine

Crush can use an external diff program to produce higher‑quality minimal diffs.
By default (when available), Git is used with histogram+minimal heuristics. You
can also customize the command.

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "diff": {
      "external_command": "git diff --no-index --histogram --minimal -U3 -- a {old} -- b {new}",
      "parse_mode": "unified"
    }
  }
}
```

Enable word-level parsing (requires Git):

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "diff": {
      "external_command": "git diff --no-index --histogram --minimal --word-diff=porcelain -U3 -- a {old} -- b {new}",
      "parse_mode": "git_word_porcelain"
    }
  }
}
```

- external_command: Shell command template with placeholders:
  - {old}: path to a temp file with the "before" content
  - {new}: path to a temp file with the "after" content
  - Defaults to Git when unset (if `git` is installed). If unavailable, Crush falls back to a built‑in diff.
- parse_mode:
  - unified (default): treat output as unified diff text
  - git_word_porcelain: expect `--word-diff=porcelain` and parse word‑level changes (requires Git)
  - auto: detect porcelain automatically when present in the command, else treat as unified

Note: word‑level parsing is opt‑in; set parse_mode to `git_word_porcelain` to enable it.

### Ignoring Files

Crush respects `.gitignore` files by default, but you can also create a
`.crushignore` file to specify additional files and directories that Crush
should ignore. This is useful for excluding files that you want in version
control but don't want Crush to consider when providing context.

The `.crushignore` file uses the same syntax as `.gitignore` and can be placed
in the root of your project or in subdirectories.

### Allowing Tools

By default, Crush will ask you for permission before running tool calls. If
you'd like, you can allow tools to be executed without prompting you for
permissions. Use this with care.

```json
{
  "$schema": "https://charm.land/crush.json",
  "permissions": {
    "allowed_tools": [
      "view",
      "ls",
      "grep",
      "edit",
      "mcp_context7_get-library-doc"
    ]
  }
}
```

You can also skip all permission prompts entirely by running Crush with the
`--yolo` flag. Be very, very careful with this feature.

### Local Models

Local models can also be configured via OpenAI-compatible API. Here are two common examples:

#### Ollama

```json
{
  "providers": {
    "ollama": {
      "name": "Ollama",
      "base_url": "http://localhost:11434/v1/",
      "type": "openai",
      "models": [
        {
          "name": "Qwen 3 30B",
          "id": "qwen3:30b",
          "context_window": 256000,
          "default_max_tokens": 20000
        }
      ]
    }
  }
}
```

#### LM Studio

```json
{
  "providers": {
    "lmstudio": {
      "name": "LM Studio",
      "base_url": "http://localhost:1234/v1/",
      "type": "openai",
      "models": [
        {
          "name": "Qwen 3 30B",
          "id": "qwen/qwen3-30b-a3b-2507",
          "context_window": 256000,
          "default_max_tokens": 20000
        }
      ]
    }
  }
}
```

### System Prompt Overrides and Prefix

Crush lets you override the coder agent’s built‑in system prompt per provider and optionally prepend a short prefix.

- `providers[].system_prompt_path`: Absolute, `~`, or relative path to a file whose contents replace the built‑in coder prompt.
  - `~` expands to home; `$VARS` are expanded; relative paths resolve against the project’s working directory.
  - The loaded text is followed by Crush’s environment info and any configured project context files.
- `providers[].system_prompt_prefix`: A short string prepended to the system prompt at runtime.
  - OpenAI/Gemini: `prefix + "\n" + system prompt text`
  - Anthropic: prefix is sent as a separate system block, before the main system prompt block

Example:

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "openai": {
      "type": "openai",
      "api_key": "$OPENAI_API_KEY",
      "system_prompt_path": "./prompts/coder.md",
      "system_prompt_prefix": "# Team Policy\nBe concise; prefer minimal diffs."
    }
  }
}
```

Notes
- This override currently applies to the coder agent. Title and summarizer prompts still use their built‑ins.
- If system_prompt_path cannot be read, Crush falls back to the built‑in coder prompt.

### Custom Providers

Crush supports custom provider configurations for both OpenAI-compatible and
Anthropic-compatible APIs.

#### OpenAI-Compatible APIs

Here’s an example configuration for Deepseek, which uses an OpenAI-compatible
API. Don't forget to set `DEEPSEEK_API_KEY` in your environment.

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "deepseek": {
      "type": "openai",
      "base_url": "https://api.deepseek.com/v1",
      "api_key": "$DEEPSEEK_API_KEY",
      "models": [
        {
          "id": "deepseek-chat",
          "name": "Deepseek V3",
          "cost_per_1m_in": 0.27,
          "cost_per_1m_out": 1.1,
          "cost_per_1m_in_cached": 0.07,
          "cost_per_1m_out_cached": 1.1,
          "context_window": 64000,
          "default_max_tokens": 5000
        }
      ]
    }
  }
}
```

#### Anthropic-Compatible APIs

Custom Anthropic-compatible providers follow this format:

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "custom-anthropic": {
      "type": "anthropic",
      "base_url": "https://api.anthropic.com/v1",
      "api_key": "$ANTHROPIC_API_KEY",
      "extra_headers": {
        "anthropic-version": "2023-06-01"
      },
      "models": [
        {
          "id": "claude-sonnet-4-20250514",
          "name": "Claude Sonnet 4",
          "cost_per_1m_in": 3,
          "cost_per_1m_out": 15,
          "cost_per_1m_in_cached": 3.75,
          "cost_per_1m_out_cached": 0.3,
          "context_window": 200000,
          "default_max_tokens": 50000,
          "can_reason": true,
          "supports_attachments": true
        }
      ]
    }
  }
}
```

### Amazon Bedrock

Crush currently supports running Anthropic models through Bedrock, with caching disabled.

* A Bedrock provider will appear once you have AWS configured, i.e. `aws configure`
* Crush also expects the `AWS_REGION` or `AWS_DEFAULT_REGION` to be set
* To use a specific AWS profile set `AWS_PROFILE` in your environment, i.e. `AWS_PROFILE=myprofile crush`

### Vertex AI Platform

Vertex AI will appear in the list of available providers when `VERTEXAI_PROJECT` and `VERTEXAI_LOCATION` are set. You will also need to be authenticated:

```bash
gcloud auth application-default login
```

To add specific models to the configuration, configure as such:

```json
{
  "$schema": "https://charm.land/crush.json",
  "providers": {
    "vertexai": {
      "models": [
        {
          "id": "claude-sonnet-4@20250514",
          "name": "VertexAI Sonnet 4",
          "cost_per_1m_in": 3,
          "cost_per_1m_out": 15,
          "cost_per_1m_in_cached": 3.75,
          "cost_per_1m_out_cached": 0.3,
          "context_window": 200000,
          "default_max_tokens": 50000,
          "can_reason": true,
          "supports_attachments": true
        }
      ]
    }
  }
}
```

## A Note on Claude Max and GitHub Copilot

Crush only supports model providers through official, compliant APIs. We do not
support or endorse any methods that rely on personal Claude Max and GitHub Copilot
accounts or OAuth workarounds, which may violate Anthropic and Microsoft’s
Terms of Service.

We’re committed to building sustainable, trusted integrations with model
providers. If you’re a provider interested in working with us, 
[reach out](mailto:vt100@charm.sh).

## Logging

Sometimes you need to look at logs. Luckily, Crush logs all sorts of
stuff. Logs are stored in `./.crush/logs/crush.log` relative to the project.

The CLI also contains some helper commands to make perusing recent logs easier:

```bash
# Print the last 1000 lines
crush logs

# Print the last 500 lines
crush logs --tail 500

# Follow logs in real time
crush logs --follow
```

Want more logging? Run `crush` with the `--debug` flag, or enable it in the
config:

```json
{
  "$schema": "https://charm.land/crush.json",
  "options": {
    "debug": true,
    "debug_lsp": true
  }
}
```

## Development

Set up a local dev environment with the tools our repo expects.

Required tools
- Go 1.24+ (https://go.dev/dl/)
- golangci-lint (for linting)
- gofumpt (formatter)
- Task (task runner)
- pre-commit (git hooks)

macOS (Homebrew)
```bash
brew install go golangci-lint gofumpt pre-commit go-task/tap/go-task
```

Linux
```bash
# Go: download from https://go.dev/dl/ or your distro
# Task (if not packaged):
go install github.com/go-task/task/v3/cmd/task@latest
# gofumpt
 go install mvdan.cc/gofumpt@latest
# golangci-lint (official installer → GOPATH/bin)
curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh \
  | sh -s -- -b "$(go env GOPATH)/bin"
# pre-commit
pipx install pre-commit || pip install --user pre-commit
```

Windows
- Install Go from https://go.dev/dl/
- Install Scoop: https://scoop.sh/
- Then:
```powershell
scoop install golangci-lint pre-commit
# Task and gofumpt via `go install`:
$env:GOBIN = "$env:USERPROFILE\go\bin"
go install github.com/go-task/task/v3/cmd/task@latest
 go install mvdan.cc/gofumpt@latest
```

PATH note
```bash
# Ensure GOPATH/bin is on your PATH (Linux/macOS)
echo 'export PATH="$(go env GOPATH)/bin:$PATH"' >> ~/.bashrc  # or zshrc/fish equivalent
```

Project tasks
```bash
# Install git hooks
 task pre-commit:setup
# Lint and auto-fix
 task lint
 task lint-fix
# Run tests
 task test
```

## Whatcha think?

We’d love to hear your thoughts on this project. Need help? We gotchu. You can find us on:

- [Twitter](https://twitter.com/charmcli)
- [Discord][discord]
- [Slack](https://charm.land/slack)
- [The Fediverse](https://mastodon.social/@charmcli)

[discord]: https://charm.land/discord

## License

[FSL-1.1-MIT](https://github.com/charmbracelet/crush/raw/main/LICENSE)

---

Part of [Charm](https://charm.land).

<a href="https://charm.land/"><img alt="The Charm logo" width="400" src="https://stuff.charm.sh/charm-banner-next.jpg" /></a>

<!--prettier-ignore-->
Charm热爱开源 • Charm loves open source
