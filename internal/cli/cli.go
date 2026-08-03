package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

var version = "dev"

type config struct {
	Version        int                      `yaml:"version"`
	CurrentContext string                   `yaml:"current-context"`
	Defaults       defaultsConfig           `yaml:"defaults"`
	Contexts       map[string]contextConfig `yaml:"contexts"`
}

type defaultsConfig struct {
	OutputDir    string `yaml:"output_dir"`
	AIAPIVersion string `yaml:"ai_api_version"`
	Format       string `yaml:"format"`
	Attachments  bool   `yaml:"attachments"`
	Copy         bool   `yaml:"copy"`
}

type contextConfig struct {
	AIAPIURL            string `yaml:"ai_api_url"`
	AIAuthURL           string `yaml:"ai_auth_url"`
	AIAPIUsername       string `yaml:"ai_api_username"`
	AIAPIPassword       string `yaml:"ai_api_password"`
	ClaimHubBaseURL     string `yaml:"claimhub_base_url"`
	ClaimHubAPIURL      string `yaml:"claimhub_api_url"`
	ClaimHubAPIToken    string `yaml:"claimhub_api_token"`
	ClaimHubTenant      string `yaml:"claimhub_tenant"`
	ClaimHubCountryCode string `yaml:"claimhub_country_code"`
	ClaimHubEmail       string `yaml:"claimhub_email"`
	ClaimHubPassword    string `yaml:"claimhub_password"`
}

type runtimeConfig struct {
	configPath  string
	configDir   string
	cfg         config
	contextName string
	context     contextConfig
	outputDir   string
	cookieFile  string
	client      *http.Client
}

type cookieEntry struct {
	Cookies   map[string]string `json:"cookies"`
	ExpiresAt string            `json:"expires_at"`
}

func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[claim] "+format+"\n", args...)
}

func Run(args []string) error {
	if len(args) == 0 {
		printRootHelp()
		return nil
	}

	root := flag.NewFlagSet("claim", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	configPath := root.String("config", resolveDefaultConfigPath(), "config path")
	contextName := root.String("context", "", "context override")
	if err := root.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printRootHelp()
			return nil
		}
		return err
	}

	remaining := root.Args()
	if len(remaining) == 0 {
		return usageError()
	}

	switch remaining[0] {
	case "context":
		return runContextCommand(*configPath, remaining[1:])
	case "contexts":
		return runContextCommand(*configPath, []string{"list"})
	case "current":
		return runContextCommand(*configPath, []string{"current"})
	case "use":
		return runContextCommand(*configPath, append([]string{"use"}, remaining[1:]...))
	case "download":
		return runDownloadCommand(*configPath, *contextName, remaining[1:])
	case "get":
		return runGetCommand(*configPath, *contextName, remaining[1:])
	case "resolve":
		return runResolveCommand(*configPath, *contextName, remaining[1:])
	case "version":
		fmt.Printf("claim-cli %s\n", version)
		return nil
	default:
		return usageError()
	}
}

func usageError() error {
	printRootHelp()
	return nil
}

func printRootHelp() {
	fmt.Println("claim")
	fmt.Println("  Everyday ClaimHub + AI helper")
	fmt.Printf("  version %s\n", version)
	fmt.Println()
	fmt.Println("Usage")
	fmt.Println("  claim [--config PATH] [--context NAME] <command> [options]")
	fmt.Println()
	fmt.Println("Context Commands")
	fmt.Println("  current                Show the current context")
	fmt.Println("  contexts               List all contexts")
	fmt.Println("  use NAME               Switch current context")
	fmt.Println("  context current        Show the current context")
	fmt.Println("  context list           List all contexts")
	fmt.Println("  context use NAME       Switch current context")
	fmt.Println()
	fmt.Println("Claim Commands")
	fmt.Println("  get                    Fetch a claim payload")
	fmt.Println("  resolve                Resolve claim id <-> claim no")
	fmt.Println("  download               Download claim attachment(s)")
	fmt.Println("  version                Show CLI version")
}

func runContextCommand(configPath string, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: claim context <list|current|use>")
	}

	cfg, err := readConfig(configPath)
	if err != nil {
		return err
	}

	switch args[0] {
	case "list":
		names := make([]string, 0, len(cfg.Contexts))
		for name := range cfg.Contexts {
			names = append(names, name)
		}
		sort.Strings(names)
		nameWidth := len("NAME")
		tenantWidth := len("TENANT")
		for _, name := range names {
			ctx := cfg.Contexts[name]
			if len(name) > nameWidth {
				nameWidth = len(name)
			}
			if len(ctx.ClaimHubTenant) > tenantWidth {
				tenantWidth = len(ctx.ClaimHubTenant)
			}
		}
		fmt.Printf("  %-*s  %-*s  %s\n", nameWidth, "NAME", tenantWidth, "TENANT", "AI API URL")
		for _, name := range names {
			prefix := "  "
			if name == cfg.CurrentContext {
				prefix = "* "
			}
			ctx := cfg.Contexts[name]
			fmt.Printf("%s%-*s  %-*s  %s\n", prefix, nameWidth, name, tenantWidth, ctx.ClaimHubTenant, ctx.AIAPIURL)
		}
		return nil
	case "current":
		fmt.Printf("Current context: %s\n", cfg.CurrentContext)
		return nil
	case "use":
		if len(args) != 2 {
			return errors.New("usage: claim context use NAME")
		}
		if _, ok := cfg.Contexts[args[1]]; !ok {
			return fmt.Errorf("unknown context %q", args[1])
		}
		cfg.CurrentContext = args[1]
		if err := writeConfig(configPath, cfg); err != nil {
			return err
		}
		fmt.Printf("Switched to context %q.\n", args[1])
		return nil
	default:
		return errors.New("usage: claim context <list|current|use>")
	}
}

func runGetCommand(configPath, contextOverride string, args []string) error {
	fs := flag.NewFlagSet("get", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	claimID := fs.Int("i", 0, "claim id")
	claimNo := fs.Int("n", 0, "claim number")
	format := fs.String("format", "", "raw|internal|summary")
	attachments := fs.Bool("attachments", false, "include attachments")
	noAttachments := fs.Bool("no-attachments", false, "exclude attachments")
	copyOutput := fs.Bool("copy", false, "copy output to clipboard")
	saveOutput := fs.Bool("save", false, "save output to file")
	outputDir := fs.String("output-dir", "", "output directory")
	timeout := fs.Float64("timeout", 60, "timeout seconds")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("Usage")
			fmt.Println("  claim get (-i CLAIM_ID | -n CLAIM_NO) [--format raw|internal|summary]")
			fmt.Println("            [--attachments|--no-attachments] [--copy] [--save]")
			fmt.Println("            [--output-dir DIR] [--timeout SECONDS]")
			return nil
		}
		return err
	}
	if (*claimID == 0) == (*claimNo == 0) {
		return errors.New("provide exactly one of -i or -n")
	}

	rt, err := loadRuntime(configPath, contextOverride, *outputDir, *timeout)
	if err != nil {
		return err
	}
	claimsOutputDir := resolveClaimsOutputDir(rt, *outputDir)

	selectedFormat := strings.TrimSpace(*format)
	if selectedFormat == "" {
		selectedFormat = rt.cfg.Defaults.Format
	}
	if selectedFormat == "" {
		selectedFormat = "raw"
	}
	if selectedFormat != "raw" && selectedFormat != "internal" && selectedFormat != "summary" {
		return fmt.Errorf("unsupported format %q", selectedFormat)
	}

	if *attachments && *noAttachments {
		return errors.New("provide at most one of --attachments or --no-attachments")
	}
	includeAttachments := rt.cfg.Defaults.Attachments
	if *attachments {
		includeAttachments = true
	}
	if *noAttachments {
		includeAttachments = false
	}
	shouldCopyOutput := rt.cfg.Defaults.Copy
	if *copyOutput {
		shouldCopyOutput = true
	}
	logf("context=%s claimhub_tenant=%s ai=%s format=%s attachments=%t copy=%t", rt.contextName, rt.context.ClaimHubTenant, rt.context.AIAPIURL, selectedFormat, includeAttachments, shouldCopyOutput)

	claim, claimRaw, err := fetchClaim(rt, *claimID, *claimNo)
	if err != nil {
		return err
	}

	var attachmentsRaw []json.RawMessage
	if includeAttachments {
		atts, attsRaw, err := fetchAttachments(rt, claim)
		if err != nil {
			return err
		}
		claim["attachments"] = atts
		attachmentsRaw = attsRaw
	}

	var result any = claim
	if selectedFormat == "raw" {
		finalRaw := claimRaw
		if includeAttachments {
			finalRaw, err = injectRawField(finalRaw, "attachments", marshalRawArray(attachmentsRaw))
			if err != nil {
				return err
			}
		}
		result = json.RawMessage(finalRaw)
	} else {
		converted, err := toInternalClaim(rt, claim, selectedFormat == "summary")
		if err != nil {
			return err
		}
		result = converted
	}

	outputText, err := renderOutput(result, selectedFormat)
	if err != nil {
		return err
	}

	if *saveOutput {
		path, err := saveOutputFile(claimsOutputDir, result)
		if err != nil {
			return err
		}
		logf("saved claim output to %s", path)
		fmt.Fprintf(os.Stderr, "Saved: %s\n", path)
	}

	if shouldCopyOutput {
		if copyToClipboard(outputText) {
			fmt.Fprintln(os.Stderr, "Copied to clipboard")
		} else {
			fmt.Fprintln(os.Stderr, "Clipboard unavailable")
		}
		fmt.Print(renderPreview(outputText, 12))
		if !strings.HasSuffix(outputText, "\n") {
			fmt.Println()
		}
		return nil
	}

	fmt.Print(outputText)
	if !strings.HasSuffix(outputText, "\n") {
		fmt.Println()
	}
	return nil
}

func runResolveCommand(configPath, contextOverride string, args []string) error {
	fs := flag.NewFlagSet("resolve", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	claimID := fs.Int("i", 0, "claim id")
	claimNo := fs.Int("n", 0, "claim number")
	timeout := fs.Float64("timeout", 60, "timeout seconds")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("Usage")
			fmt.Println("  claim resolve (-i CLAIM_ID | -n CLAIM_NO) [--timeout SECONDS]")
			return nil
		}
		return err
	}
	if (*claimID == 0) == (*claimNo == 0) {
		return errors.New("provide exactly one of -i or -n")
	}

	rt, err := loadRuntime(configPath, contextOverride, "", *timeout)
	if err != nil {
		return err
	}
	logf("context=%s claimhub_tenant=%s resolve", rt.contextName, rt.context.ClaimHubTenant)

	claim, _, err := fetchClaim(rt, *claimID, *claimNo)
	if err != nil {
		return err
	}

	if *claimID != 0 {
		claimNoValue, ok := asInt(claim["no"])
		if !ok {
			return fmt.Errorf("claim %d has no claim number", *claimID)
		}
		fmt.Println(claimNoValue)
		return nil
	}

	claimIDValue, ok := asInt(claim["id"])
	if !ok {
		return fmt.Errorf("claim no %d has no claim id", *claimNo)
	}
	fmt.Println(claimIDValue)
	return nil
}

func runDownloadCommand(configPath, contextOverride string, args []string) error {
	fs := flag.NewFlagSet("download", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	secureID := fs.String("secure-id", "", "attachment secure id")
	claimID := fs.Int("i", 0, "claim id")
	claimNo := fs.Int("n", 0, "claim number")
	name := fs.String("name", "", "attachment filename")
	attachmentID := fs.Int("att-id", 0, "attachment id")
	outputDir := fs.String("output-dir", "", "output directory")
	timeout := fs.Float64("timeout", 60, "timeout seconds")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Println("Usage")
			fmt.Println("  claim download (--secure-id ID | -i CLAIM_ID | -n CLAIM_NO)")
			fmt.Println("                 [--name FILE] [--att-id ID] [--output-dir DIR] [--timeout SECONDS]")
			return nil
		}
		return err
	}

	refCount := 0
	if strings.TrimSpace(*secureID) != "" {
		refCount++
	}
	if *claimID != 0 {
		refCount++
	}
	if *claimNo != 0 {
		refCount++
	}
	if refCount != 1 {
		return errors.New("provide exactly one of --secure-id, -i, or -n")
	}

	rt, err := loadRuntime(configPath, contextOverride, "", *timeout)
	if err != nil {
		return err
	}
	baseOutputDir := resolveAttachmentsOutputDir(rt, *outputDir)
	logf("context=%s claimhub_tenant=%s download output=%s", rt.contextName, rt.context.ClaimHubTenant, baseOutputDir)

	if strings.TrimSpace(*secureID) != "" {
		logf("downloading attachment by secure_id=%s", strings.TrimSpace(*secureID))
		path, err := downloadBySecureID(rt, strings.TrimSpace(*secureID), baseOutputDir, strings.TrimSpace(*secureID))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Saved: %s\n", path)
		return nil
	}

	claim, _, err := fetchClaim(rt, *claimID, *claimNo)
	if err != nil {
		return err
	}
	attachments, _, err := fetchAttachments(rt, claim)
	if err != nil {
		return err
	}
	logf("found %d attachment(s)", len(attachments))
	if len(attachments) == 0 {
		return errors.New("no attachments found")
	}

	if strings.TrimSpace(*name) != "" {
		filtered := []map[string]any{}
		for _, attachment := range attachments {
			if attachmentName(attachment) == strings.TrimSpace(*name) {
				filtered = append(filtered, attachment)
			}
		}
		attachments = filtered
		if len(attachments) == 0 {
			return fmt.Errorf("no attachment named %q", *name)
		}
	} else if *attachmentID != 0 {
		filtered := []map[string]any{}
		for _, attachment := range attachments {
			if id, ok := asInt(attachment["id"]); ok && id == *attachmentID {
				filtered = append(filtered, attachment)
			}
		}
		attachments = filtered
		if len(attachments) == 0 {
			return fmt.Errorf("no attachment with id %d", *attachmentID)
		}
	} else {
		selected, err := interactivePick(attachments)
		if err != nil {
			return err
		}
		attachments = selected
	}

	claimDirName := "claim"
	if id, ok := asInt(claim["id"]); ok {
		claimDirName = strconv.Itoa(id)
	} else if no, ok := asInt(claim["no"]); ok {
		claimDirName = strconv.Itoa(no)
	}
	attOutputDir := filepath.Join(baseOutputDir, claimDirName)
	for _, attachment := range attachments {
		secureIDValue, _ := attachment["secureId"].(string)
		path, err := downloadBySecureID(rt, secureIDValue, attOutputDir, attachmentName(attachment))
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "Saved: %s\n", path)
	}
	return nil
}

func resolveDefaultConfigPath() string {
	if fromEnv := strings.TrimSpace(os.Getenv("EVERYDAY_CLAIM_CONFIG")); fromEnv != "" {
		return fromEnv
	}
	candidates := []string{"config.yaml"}
	if configDir, err := os.UserConfigDir(); err == nil {
		candidates = append(candidates, filepath.Join(configDir, "claim-cli", "config.yaml"))
	}
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	if len(candidates) > 1 {
		return candidates[1]
	}
	return candidates[0]
}

func readConfig(path string) (config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if mkdirErr := os.MkdirAll(filepath.Dir(path), 0o755); mkdirErr == nil {
				return config{}, fmt.Errorf("config not found at %s\ncreate it from config.example.yaml", path)
			}
		}
		return config{}, err
	}
	var cfg config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return config{}, err
	}
	if cfg.Contexts == nil {
		return config{}, errors.New("config has no contexts")
	}
	return cfg, nil
}

func writeConfig(path string, cfg config) error {
	b, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, b, 0o644)
}

func loadRuntime(configPath, contextOverride, outputOverride string, timeout float64) (runtimeConfig, error) {
	cfg, err := readConfig(configPath)
	if err != nil {
		return runtimeConfig{}, err
	}
	selectedContext := strings.TrimSpace(contextOverride)
	if selectedContext == "" {
		selectedContext = strings.TrimSpace(cfg.CurrentContext)
	}
	if selectedContext == "" {
		return runtimeConfig{}, errors.New("no current context set")
	}
	ctx, ok := cfg.Contexts[selectedContext]
	if !ok {
		return runtimeConfig{}, fmt.Errorf("unknown context %q", selectedContext)
	}
	configDir, err := filepath.Abs(filepath.Dir(configPath))
	if err != nil {
		return runtimeConfig{}, err
	}
	outputDir := strings.TrimSpace(outputOverride)
	if outputDir == "" {
		if configuredOutputDir := strings.TrimSpace(cfg.Defaults.OutputDir); configuredOutputDir != "" {
			outputDir = configuredOutputDir
		} else {
			outputDir, err = defaultDataDir()
			if err != nil {
				return runtimeConfig{}, err
			}
		}
	}
	if outputDir == "" {
		outputDir, err = defaultDataDir()
		if err != nil {
			return runtimeConfig{}, err
		}
	}
	if !filepath.IsAbs(outputDir) {
		outputDir = filepath.Join(configDir, outputDir)
	}
	cookieFile, err := defaultCookieFile()
	if err != nil {
		return runtimeConfig{}, err
	}
	return runtimeConfig{
		configPath:  configPath,
		configDir:   configDir,
		cfg:         cfg,
		contextName: selectedContext,
		context:     ctx,
		outputDir:   outputDir,
		cookieFile:  cookieFile,
		client:      &http.Client{Timeout: time.Duration(timeout * float64(time.Second))},
	}, nil
}

func defaultDataDir() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		return filepath.Join(homeDir, "Library", "Application Support", "claim-cli"), nil
	}
	if xdgDataHome := strings.TrimSpace(os.Getenv("XDG_DATA_HOME")); xdgDataHome != "" {
		return filepath.Join(xdgDataHome, "claim-cli"), nil
	}
	return filepath.Join(homeDir, ".local", "share", "claim-cli"), nil
}

func defaultCookieFile() (string, error) {
	if runtime.GOOS == "darwin" {
		homeDir, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(homeDir, "Library", "Caches", "claim-cli", "cookies.json"), nil
	}
	cacheDir, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cacheDir, "claim-cli", "cookies.json"), nil
}

func resolveClaimsOutputDir(rt runtimeConfig, outputOverride string) string {
	if strings.TrimSpace(outputOverride) != "" {
		if filepath.IsAbs(outputOverride) {
			return outputOverride
		}
		return filepath.Join(rt.configDir, outputOverride)
	}
	return filepath.Join(rt.outputDir, "claims")
}

func resolveAttachmentsOutputDir(rt runtimeConfig, outputOverride string) string {
	if strings.TrimSpace(outputOverride) != "" {
		if filepath.IsAbs(outputOverride) {
			return outputOverride
		}
		return filepath.Join(rt.configDir, outputOverride)
	}
	return filepath.Join(rt.outputDir, "attachments")
}

func fetchClaim(rt runtimeConfig, claimID, claimNo int) (map[string]any, []byte, error) {
	if claimID != 0 {
		return fetchClaimByID(rt, claimID)
	}
	return fetchClaimByNo(rt, claimNo)
}

func fetchClaimByID(rt runtimeConfig, claimID int) (map[string]any, []byte, error) {
	logf("fetching claim by id=%d via %s/p/api/insurance/%d", claimID, strings.TrimRight(rt.context.ClaimHubBaseURL, "/"), claimID)
	req, err := http.NewRequest(http.MethodGet, strings.TrimRight(rt.context.ClaimHubBaseURL, "/")+fmt.Sprintf("/p/api/insurance/%d", claimID), nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("X-AUTH-TOKEN", rt.context.ClaimHubAPIToken)
	raw, err := doJSONRaw(rt.client, req)
	if err != nil {
		return nil, nil, err
	}
	var claim map[string]any
	if err := json.Unmarshal(raw, &claim); err != nil {
		return nil, nil, err
	}
	if claim == nil {
		return nil, nil, fmt.Errorf("claim %d not found", claimID)
	}
	logf("claim fetch succeeded for id=%d", claimID)
	return claim, raw, nil
}

func fetchClaimByNo(rt runtimeConfig, claimNo int) (map[string]any, []byte, error) {
	logf("fetching claim by no=%d via %s/x/p/s/%s/%s/insurance/%d", claimNo, strings.TrimRight(rt.context.ClaimHubBaseURL, "/"), rt.context.ClaimHubTenant, rt.context.ClaimHubCountryCode, claimNo)
	path := fmt.Sprintf("/x/p/s/%s/%s/insurance/%d", rt.context.ClaimHubTenant, rt.context.ClaimHubCountryCode, claimNo)
	raw, err := doTenantJSONRaw(rt, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, nil, err
	}
	var claim map[string]any
	if err := json.Unmarshal(raw, &claim); err != nil {
		return nil, nil, err
	}
	if claim == nil {
		return nil, nil, fmt.Errorf("claim no %d not found", claimNo)
	}
	logf("claim fetch succeeded for no=%d", claimNo)
	return claim, raw, nil
}

func fetchAttachments(rt runtimeConfig, claim map[string]any) ([]map[string]any, []json.RawMessage, error) {
	claimNo, ok := asInt(claim["no"])
	if !ok {
		claimID, idOK := asInt(claim["id"])
		if !idOK {
			return nil, nil, errors.New("claim has no id or no field")
		}
		resolved, _, err := fetchClaimByID(rt, claimID)
		if err != nil {
			return nil, nil, err
		}
		claimNo, ok = asInt(resolved["no"])
		if !ok {
			return nil, nil, fmt.Errorf("claim %d has no claim number", claimID)
		}
	}
	path := fmt.Sprintf("/x/p/s/%s/%s/insurance/%d/attachments", rt.context.ClaimHubTenant, rt.context.ClaimHubCountryCode, claimNo)
	logf("fetching attachments for claim_no=%d", claimNo)
	raw, err := doTenantJSONRaw(rt, http.MethodGet, path, nil, false)
	if err != nil {
		return nil, nil, err
	}
	attachments, rawItems, err := attachmentItemsFromRaw(raw)
	if err != nil {
		return nil, nil, err
	}
	logf("attachments fetch succeeded for claim_no=%d count=%d", claimNo, len(attachments))
	return attachments, rawItems, nil
}

func downloadBySecureID(rt runtimeConfig, secureID, outputDir, fileName string) (string, error) {
	if strings.TrimSpace(secureID) == "" {
		return "", errors.New("empty secure id")
	}
	path := fmt.Sprintf("/x/p/s/%s/%s/attachment/%s/download", rt.context.ClaimHubTenant, rt.context.ClaimHubCountryCode, secureID)
	logf("downloading secure_id=%s", secureID)
	data, err := doTenantBytes(rt, http.MethodGet, path, false)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(fileName) == "" {
		fileName = secureID
	}
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	filePath := filepath.Join(outputDir, fileName)
	if err := os.WriteFile(filePath, data, 0o644); err != nil {
		return "", err
	}
	logf("download saved secure_id=%s bytes=%d path=%s", secureID, len(data), filePath)
	return filePath, nil
}

func doJSON(client *http.Client, req *http.Request, target any) error {
	raw, err := doJSONRaw(client, req)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func doJSONRaw(client *http.Client, req *http.Request) ([]byte, error) {
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("request failed: status=%d preview=%q", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

func doTenantJSON(rt runtimeConfig, method, path string, body io.Reader, target any, retried bool) error {
	raw, err := doTenantJSONRaw(rt, method, path, body, retried)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func doTenantJSONRaw(rt runtimeConfig, method, path string, body io.Reader, retried bool) ([]byte, error) {
	cookies, err := loadTenantCookies(rt, retried)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, strings.TrimRight(rt.context.ClaimHubBaseURL, "/")+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader(cookies))
	resp, err := rt.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if retried {
			bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("tenant request failed: status=%d preview=%q", resp.StatusCode, string(bodyPreview))
		}
		logf("tenant request got status=%d, refreshing session cookie and retrying", resp.StatusCode)
		_, _ = loadTenantCookies(rt, true)
		return doTenantJSONRaw(rt, method, path, body, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tenant request failed: status=%d preview=%q", resp.StatusCode, string(bodyPreview))
	}
	return io.ReadAll(resp.Body)
}

func doTenantBytes(rt runtimeConfig, method, path string, retried bool) ([]byte, error) {
	cookies, err := loadTenantCookies(rt, retried)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(method, strings.TrimRight(rt.context.ClaimHubBaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Cookie", cookieHeader(cookies))
	resp, err := rt.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		if retried {
			bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return nil, fmt.Errorf("tenant download failed: status=%d preview=%q", resp.StatusCode, string(bodyPreview))
		}
		logf("tenant download got status=%d, refreshing session cookie and retrying", resp.StatusCode)
		_, _ = loadTenantCookies(rt, true)
		return doTenantBytes(rt, method, path, true)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("tenant download failed: status=%d preview=%q", resp.StatusCode, string(bodyPreview))
	}
	return io.ReadAll(resp.Body)
}

func loadTenantCookies(rt runtimeConfig, forceRefresh bool) (map[string]string, error) {
	if !forceRefresh {
		cached, err := readCookieEntry(rt.cookieFile, rt.contextName)
		if err == nil && time.Now().Before(cached.expiresAt()) && len(cached.Cookies) > 0 {
			logf("using cached ClaimHub session cookie for context=%s", rt.contextName)
			return cached.Cookies, nil
		}
	}
	if forceRefresh {
		logf("refreshing ClaimHub session cookie for context=%s", rt.contextName)
	} else {
		logf("logging in to ClaimHub for context=%s", rt.contextName)
	}
	form := url.Values{}
	form.Set("email", rt.context.ClaimHubEmail)
	form.Set("password", rt.context.ClaimHubPassword)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(rt.context.ClaimHubBaseURL, "/")+"/x/p/login", strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := rt.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		bodyPreview, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("claimhub login failed: status=%d preview=%q", resp.StatusCode, string(bodyPreview))
	}
	cookies := map[string]string{}
	var expiresAt time.Time
	for _, cookie := range resp.Cookies() {
		cookies[cookie.Name] = cookie.Value
		if cookie.Name == "r" || cookie.Name == "r2" {
			if !cookie.Expires.IsZero() {
				expiresAt = cookie.Expires.Add(-time.Minute)
			} else {
				expiresAt = time.Now().Add(24 * time.Hour)
			}
		}
	}
	if expiresAt.IsZero() || len(cookies) == 0 {
		return nil, errors.New("claimhub login succeeded but no session cookie was returned")
	}
	entry := cookieEntry{Cookies: cookies, ExpiresAt: expiresAt.Format(time.RFC3339)}
	if err := writeCookieEntry(rt.cookieFile, rt.contextName, entry); err != nil {
		return nil, err
	}
	logf("ClaimHub session cookie ready for context=%s", rt.contextName)
	return cookies, nil
}

func readCookieEntry(path, key string) (cookieEntry, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return cookieEntry{}, err
	}
	data := map[string]cookieEntry{}
	if err := json.Unmarshal(b, &data); err != nil {
		return cookieEntry{}, err
	}
	entry, ok := data[key]
	if !ok {
		return cookieEntry{}, errors.New("cookie entry not found")
	}
	return entry, nil
}

func writeCookieEntry(path, key string, entry cookieEntry) error {
	data := map[string]cookieEntry{}
	b, err := os.ReadFile(path)
	if err == nil {
		_ = json.Unmarshal(b, &data)
	}
	data[key] = entry
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := json.MarshalIndent(data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

func (e cookieEntry) expiresAt() time.Time {
	parsed, err := time.Parse(time.RFC3339, e.ExpiresAt)
	if err != nil {
		return time.Time{}
	}
	return parsed
}

func cookieHeader(cookies map[string]string) string {
	parts := make([]string, 0, len(cookies))
	for name, value := range cookies {
		parts = append(parts, name+"="+value)
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}

func toInternalClaim(rt runtimeConfig, claim map[string]any, summary bool) (map[string]any, error) {
	if summary {
		return nil, errors.New("summary format is not supported by the current Hub API")
	}

	token, err := fetchAIToken(rt)
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(map[string]any{"claim": claim})
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(rt.context.AIAPIURL, "/") + "/api/" + rt.cfg.Defaults.AIAPIVersion + "/claims/claim-data"
	if strings.TrimSpace(rt.cfg.Defaults.AIAPIVersion) == "" {
		endpoint = strings.TrimRight(rt.context.AIAPIURL, "/") + "/api/v1/claim/claim-data"
	}
	logf("calling AI internal conversion via %s", endpoint)
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var result map[string]any
	if err := doJSON(rt.client, req, &result); err != nil {
		return nil, err
	}
	return result, nil
}

func fetchAIToken(rt runtimeConfig) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "password")
	form.Set("username", rt.context.AIAPIUsername)
	form.Set("password", rt.context.AIAPIPassword)
	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(rt.context.AIAuthURL, "/")+"/api/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	var payload map[string]any
	if err := doJSON(rt.client, req, &payload); err != nil {
		return "", err
	}
	token, _ := payload["access_token"].(string)
	if strings.TrimSpace(token) == "" {
		return "", errors.New("ai auth response did not contain access_token")
	}
	logf("AI auth token acquired from %s", strings.TrimRight(rt.context.AIAuthURL, "/")+"/api/v1/oauth2/token")
	return token, nil
}

func renderOutput(result any, format string) (string, error) {
	if format == "raw" || format == "summary" {
		b, err := json.MarshalIndent(result, "", "  ")
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	resultMap, ok := result.(map[string]any)
	if !ok {
		return "", errors.New("internal result is not an object")
	}
	claim, ok := resultMap["claim"].(map[string]any)
	if !ok {
		return "", errors.New("internal result has no claim object")
	}
	items, _ := claim["items"].([]any)
	claimTypes := map[string]struct{}{}
	benefitTypes := map[string]struct{}{}
	for _, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if claimType, ok := itemMap["claim_type"].(string); ok && claimType != "" {
			claimTypes[claimType] = struct{}{}
		}
		if benefitType, ok := itemMap["benefit_type"].(string); ok && benefitType != "" {
			benefitTypes[benefitType] = struct{}{}
		}
	}
	lines := []string{}
	summary, err := json.MarshalIndent(map[string]any{
		"claim_id":      claim["claim_id"],
		"peril_count":   len(items),
		"claim_types":   sortedKeys(claimTypes),
		"benefit_types": sortedKeys(benefitTypes),
	}, "", "  ")
	if err != nil {
		return "", err
	}
	lines = append(lines, string(summary))
	for index, item := range items {
		itemMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		lines = append(lines, fmt.Sprintf("\nPeril %d", index+1))
		payload, err := json.MarshalIndent(map[string]any{
			"benefit_name":      itemMap["benefit_name"],
			"benefit_type":      itemMap["benefit_type"],
			"claim_type":        itemMap["claim_type"],
			"name":              itemMap["name"],
			"incident_details":  itemMap["details"],
			"cost":              itemMap["cost"],
			"original_cost":     itemMap["original_cost"],
			"original_currency": itemMap["original_currency"],
			"date":              itemMap["date"],
			"expense_id":        itemMap["id"],
			"logged_hours":      itemMap["logged_hours"],
		}, "", "  ")
		if err != nil {
			return "", err
		}
		lines = append(lines, string(payload))
	}
	return strings.Join(lines, "\n"), nil
}

func saveOutputFile(outputDir string, result any) (string, error) {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return "", err
	}
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return "", err
	}
	name := outputName(result)
	path := filepath.Join(outputDir, name)
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

func outputName(result any) string {
	claimID := "unknown-id"
	claimNo := "unknown-no"
	resultMap, ok := result.(map[string]any)
	if !ok {
		if raw, ok := result.(json.RawMessage); ok {
			_ = json.Unmarshal(raw, &resultMap)
		}
	}
	if resultMap != nil {
		if value, ok := stringValue(resultMap["id"]); ok {
			claimID = value
		} else if claim, ok := resultMap["claim"].(map[string]any); ok {
			if value, ok := stringValue(claim["claim_id"]); ok {
				claimID = value
			}
		}
		if value, ok := stringValue(resultMap["no"]); ok {
			claimNo = value
		}
	}
	return fmt.Sprintf("claim-%s-%s.json", claimID, claimNo)
}

func renderPreview(text string, maxLines int) string {
	lines := strings.Split(text, "\n")
	if len(lines) <= maxLines {
		return text
	}
	return strings.Join(lines[:maxLines], "\n") + fmt.Sprintf("\n... (%d more lines copied)", len(lines)-maxLines)
}

func copyToClipboard(text string) bool {
	var command *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		command = exec.Command("pbcopy")
	case "windows":
		command = exec.Command("clip")
	default:
		command = exec.Command("wl-copy")
	}
	command.Stdin = strings.NewReader(text)
	return command.Run() == nil
}

func attachmentItemsFromRaw(raw []byte) ([]map[string]any, []json.RawMessage, error) {
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		var obj map[string]json.RawMessage
		if err := json.Unmarshal(raw, &obj); err != nil {
			return nil, nil, err
		}
		if wrapped, ok := obj["items"]; ok {
			if err := json.Unmarshal(wrapped, &items); err != nil {
				return nil, nil, err
			}
		} else {
			items = []json.RawMessage{json.RawMessage(raw)}
		}
	}
	decoded := make([]map[string]any, 0, len(items))
	for _, item := range items {
		var itemMap map[string]any
		if err := json.Unmarshal(item, &itemMap); err == nil {
			decoded = append(decoded, itemMap)
		}
	}
	return decoded, items, nil
}

func marshalRawArray(items []json.RawMessage) []byte {
	var buf bytes.Buffer
	buf.WriteByte('[')
	for i, item := range items {
		if i > 0 {
			buf.WriteByte(',')
		}
		buf.Write(item)
	}
	buf.WriteByte(']')
	return buf.Bytes()
}

func injectRawField(rawObj []byte, key string, value []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(rawObj)
	if len(trimmed) < 2 || trimmed[0] != '{' || trimmed[len(trimmed)-1] != '}' {
		return nil, errors.New("claim response is not a json object")
	}
	body := bytes.TrimSpace(trimmed[1 : len(trimmed)-1])
	keyJSON, err := json.Marshal(key)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	if len(body) > 0 {
		buf.Write(body)
		buf.WriteByte(',')
	}
	buf.Write(keyJSON)
	buf.WriteByte(':')
	buf.Write(value)
	buf.WriteByte('}')
	return buf.Bytes(), nil
}

func attachmentName(attachment map[string]any) string {
	fileMeta, ok := attachment["fileMeta"].(map[string]any)
	if ok {
		if name, ok := fileMeta["name"].(string); ok && strings.TrimSpace(name) != "" {
			return name
		}
	}
	if secureID, ok := attachment["secureId"].(string); ok {
		return secureID
	}
	return "attachment"
}

func interactivePick(attachments []map[string]any) ([]map[string]any, error) {
	if len(attachments) == 1 {
		return attachments, nil
	}
	fmt.Fprintln(os.Stderr)
	for index, attachment := range attachments {
		label := attachmentName(attachment)
		sizeSuffix := ""
		if fileMeta, ok := attachment["fileMeta"].(map[string]any); ok {
			if size, ok := asInt(fileMeta["size"]); ok && size > 0 {
				sizeSuffix = fmt.Sprintf("  %d KB", size/1024)
			}
		}
		fmt.Fprintf(os.Stderr, "  [%d] %s%s\n", index+1, label, sizeSuffix)
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprint(os.Stderr, "Select (e.g. 1,2 or 1-3 or \"all\"): ")
	reader := bufio.NewReader(os.Stdin)
	raw, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	input := strings.TrimSpace(raw)
	if input == "" || strings.EqualFold(input, "all") {
		return attachments, nil
	}
	selected := []map[string]any{}
	for _, part := range strings.Split(strings.ReplaceAll(input, " ", ","), ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.Contains(part, "-") {
			bounds := strings.SplitN(part, "-", 2)
			lo, err := strconv.Atoi(strings.TrimSpace(bounds[0]))
			if err != nil {
				return nil, err
			}
			hi, err := strconv.Atoi(strings.TrimSpace(bounds[1]))
			if err != nil {
				return nil, err
			}
			for idx := lo; idx <= hi; idx++ {
				if idx < 1 || idx > len(attachments) {
					return nil, fmt.Errorf("selection %d out of range", idx)
				}
				selected = append(selected, attachments[idx-1])
			}
			continue
		}
		idx, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		if idx < 1 || idx > len(attachments) {
			return nil, fmt.Errorf("selection %d out of range", idx)
		}
		selected = append(selected, attachments[idx-1])
	}
	return selected, nil
}

func sortedKeys(values map[string]struct{}) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func asInt(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		return int(typed), true
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	case string:
		parsed, err := strconv.Atoi(typed)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

func stringValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, true
	case float64:
		return strconv.Itoa(int(typed)), true
	case int:
		return strconv.Itoa(typed), true
	default:
		return "", false
	}
}
