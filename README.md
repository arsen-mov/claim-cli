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

`ai_product` and `ai_tenant` name the Lea deployment a context talks to (e.g. `claims-ai` / `hlas`).
The AI token is minted for that product and tenant; without them Hub rejects the token.

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

## Dispatch a claim to Lea AI

```bash
# fetch from ClaimHub (with attachments) and run the transformizer, wait for the result
./claim dispatch transformizer -i 31181 --wait --save

# run a claim saved earlier with `claim get --save`; `t` is short for transformizer
./claim dispatch t --file ./claims/claim-31181-4001.json --wait

# full assessment (`ass` for short); list every task with `claim dispatch list`
./claim dispatch assessment -i 31181 --wait --save

# document processing (`doc`); --overwrite reprocesses attachments that already have documents
./claim dispatch documents -i 31181 --overwrite --wait

# pre-assessment (`pre`) and document readiness (`ready`)
./claim dispatch pre -i 31181 --wait
./claim dispatch ready -i 31181 --wait   # readiness has no run status: --wait prints the current evaluation

# from a pipe or the clipboard
cat claim-31181-4001.json | ./claim dispatch t --wait
./claim get -i 31181 | ./claim dispatch t --wait
./claim dispatch t --clipboard --wait
```

`--wait` polls the task's result route by its `task_id` until SUCCESS or FAILURE and prints
the result (transformation, assessment, or the document batch). `--save` writes it under
`<output_dir>/<task>/`. Without `--wait` the command returns right after dispatch.

Release build with an explicit version:

```bash
go build -ldflags "-X claim-cli/internal/cli.version=0.1.0" -o claim ./cmd/claim
```

## From Release

Each GitHub release provides platform-specific archives.
Each archive contains a single binary named `claim`.

Typical install flow:

```bash
mkdir -p ~/.local/bin
tar -xzf claim-cli_<platform>.tar.gz
mv claim ~/.local/bin/claim
chmod +x ~/.local/bin/claim
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
./claim get -i 10129 --copy
```

`get` returns the raw ClaimHub payload. To check that Lea AI can read it, use `validate`.

Clipboard behavior:

For convenience there is copy option as sometimes we need claim body to provide as request body for Lea AI.

- `defaults.copy` in `config.yaml` controls whether `get` copies output to your clipboard by default
- if enabled, it will overwrite your current clipboard contents
- `--copy` forces clipboard copy for a single command
- to disable default clipboard copying, set `defaults.copy: false` in `config.yaml`

Validate a claim against Lea AI (`POST /api/v1/claims/claim-data`): the body must parse and the
policy in `policyInsured` must resolve. Prints the policy found and the perils Hub recognised,
or Hub's full error body on failure. Takes the same inputs as `dispatch`.

```bash
./claim validate -i 10129
cat claim-10129-123456.json | ./claim validate
./claim validate --clipboard --full   # whole normalized ClaimData
```

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
