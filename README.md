# claim-cli

Small CLI for day-to-day ClaimHub and Lea AI claim work.

## Config

By default the CLI reads `~/.config/claim-cli/config.yaml`.

If you want to keep config in the repo directory instead, you can always pass `--config ./config.yaml`.

Start from the example file:

```bash
cp config.example.yaml config.yaml
```

Then fill in the real credentials and URLs in your local `config.yaml`.

`config.yaml` is sensitive and should stay local because it contains real credentials and tokens.
Keep only `config.example.yaml` in version control. The real `config.yaml` should stay local and uncommitted.

`output_dir` is optional.

- if `output_dir` is empty or omitted, the CLI uses the OS-standard default location
- if you want a custom location, set `defaults.output_dir` in `config.yaml`

## Build

```bash
git clone git@github.com:arsen-mov/claim-cli.git
cd claim-cli
go build -o claim ./cmd/claim

./claim current
./claim contexts
./claim resolve -i 10129
./claim version
```

Release build with an explicit version:

```bash
go build -ldflags "-X claim-cli/internal/cli.version=0.1.0" -o claim ./cmd/claim
```

If you want to use `claim` directly from anywhere:

```bash
mkdir -p ~/.local/bin
git clone git@github.com:arsen-mov/claim-cli.git
cd claim-cli
go build -o ~/.local/bin/claim ./cmd/claim
```

If `~/.local/bin` is not already on your `PATH`, add this to `~/.zshrc`:

```bash
export PATH="$HOME/.local/bin:$PATH"
```

Then reload your shell:

```bash
source ~/.zshrc
```

or whatever shell that you're currently using.

After that:

```bash
claim current
claim contexts
claim resolve -i 10129
claim version
```

## Contexts

The CLI reads `config.yaml` and uses `current-context`, similar to kube context switching.

Useful commands:

```bash
./claim current
./claim contexts
./claim use starr-uat
```

You can also override the selected context for one command:

```bash
./claim --context hlas-uat resolve -i 10129
```

## Commands

Resolve claim references (`i` - claim id; root claim -> "id", `n` claim no; root claim -> "no"):

```bash
./claim resolve -i 10129
./claim resolve -n 123456
```

Fetch claims:

```bash
./claim get -i 10129
./claim get -i 10129 --save
./claim get -i 10129 --format internal --copy
```

Clipboard behavior:

For convenience there is copy option as sometimes we need claim body to provide as request body for Lea AI.

- `defaults.copy` in `config.yaml` controls whether `get` copies output to your clipboard by default
- if enabled, it will overwrite your current clipboard contents
- `--copy` forces clipboard copy for a single command
- to disable default clipboard copying, set `defaults.copy: false` in `config.yaml`

Formats:

- `raw`: the original ClaimHub claim payload as returned from ClaimHub
- `internal`: the normalized internal `ClaimData` response from `POST /api/v1/claim/claim-data`. This is useful to quickly check whether Lea AI can compile the ClaimHub claim into internal claim data, for example whether the policy exists and resolves correctly.
- `summary`: TODO - Intended meaning is a compact claim summary grouped by claim type.

Download attachments:

```bash
./claim download -i 10129
./claim download -i 10129 --att-id 31243
./claim download --secure-id abc123
```

## Output

Default output root is OS-standard, unless you override `defaults.output_dir` in `config.yaml`.

- Linux default output root: `~/.local/share/claim-cli`
- macOS default output root: `~/Library/Application Support/claim-cli`

- Saved claim payloads: `<output-root>/claims`
- Downloaded attachments: `<output-root>/attachments`
- Saved claim filename format: `claim-<claim-id>-<claim-no>.json`

ClaimHub session cookies are stored separately from output:

- Linux: `~/.cache/claim-cli/cookies.json`
- macOS: `~/Library/Caches/claim-cli/cookies.json`

`--copy` only copies rendered output to the clipboard.
`--save` writes claim payload output to disk.
