package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/net/proxy"
)

var (
	ipPortRE   = regexp.MustCompile(`\b((?:\d{1,3}\.){3}\d{1,3}):([1-9]\d{0,4})\b`)
	htmlCellRE = regexp.MustCompile(`(?is)\b((?:\d{1,3}\.){3}\d{1,3})\s*<script[^>]*>.*?</script>\s*<font[^>]*>\s*:\s*</font>\s*([1-9]\d{0,4})\s*</font>\s*</td>\s*<td[^>]*>\s*([A-Za-z0-9]+)\s*</td>`)
	rng        = rand.New(rand.NewSource(time.Now().UnixNano()))
)

type Proxy struct {
	Addr      string `json:"addr"`
	Protocol  string `json:"protocol"`
	IP        string `json:"ip"`
	Port      int    `json:"port"`
	Source    string `json:"source"`
	Alive     bool   `json:"alive,omitempty"`
	LatencyMS int64  `json:"latency_ms,omitempty"`
	Error     string `json:"error,omitempty"`
}

type fetchConfig struct {
	FlareSolverrURL string
	FlareMaxTimeout time.Duration
	PageSize        int
	Retries         int
	RetryBackoff    time.Duration
	RetryJitter     time.Duration
}

type checkConfig struct {
	Enabled        bool
	OnlyAlive      bool
	Workers        int
	ConnectTimeout time.Duration
	SOCKS5         bool
	TestURL        string
}

type appConfig struct {
	TargetURL string
	OutPath   string
	CSVOut    string
	JSONOut   bool
	Timeout   time.Duration
	Fetch     fetchConfig
	Check     checkConfig
}

type fileConfig struct {
	URL                 string `json:"url"`
	Out                 string `json:"out"`
	CSVOut              string `json:"csv_out"`
	JSON                *bool  `json:"json"`
	Timeout             string `json:"timeout"`
	FlareSolverrURL     string `json:"flaresolverr_url"`
	FlareSolverrTimeout string `json:"flaresolverr_timeout"`
	PageSize            *int   `json:"page_size"`
	Retries             *int   `json:"retries"`
	RetryBackoff        string `json:"retry_backoff"`
	RetryJitter         string `json:"retry_jitter"`
	Check               *bool  `json:"check"`
	OnlyAlive           *bool  `json:"only_alive"`
	CheckWorkers        *int   `json:"check_workers"`
	CheckTimeout        string `json:"check_timeout"`
	CheckURL            string `json:"check_url"`
	SOCKS5Handshake     *bool  `json:"socks5_handshake"`
	GUI                 *bool  `json:"gui"`
	GUIAddr             string `json:"gui_addr"`
}

func main() {
	var (
		targetURL       = flag.String("url", "https://spys.one/en/socks-proxy-list/", "target page URL")
		outPath         = flag.String("out", "proxies.txt", "output path")
		csvOut          = flag.String("csv-out", "proxies.csv", "csv output path for all proxies and checks")
		jsonOut         = flag.Bool("json", false, "output JSON")
		configPath      = flag.String("config", "", "path to JSON config file")
		timeout         = flag.Duration("timeout", 180*time.Second, "overall timeout")
		flareSolverrURL = flag.String("flaresolverr-url", "http://127.0.0.1:8191/v1", "FlareSolverr v1 endpoint")
		flareTimeout    = flag.Duration("flaresolverr-timeout", 90*time.Second, "FlareSolverr maxTimeout")
		pageSize        = flag.Int("page-size", 500, "target rows per page (30/50/100/200/300/500)")
		retries         = flag.Int("retries", 3, "max fetch attempts with backoff")
		retryBackoff    = flag.Duration("retry-backoff", 3*time.Second, "base backoff duration")
		retryJitter     = flag.Duration("retry-jitter", 1200*time.Millisecond, "max random jitter added to backoff")

		checkAlive     = flag.Bool("check", true, "run proxy availability checks")
		onlyAlive      = flag.Bool("only-alive", true, "output only alive proxies when check is enabled")
		checkWorkers   = flag.Int("check-workers", 64, "concurrent workers for availability checks")
		checkTimeout   = flag.Duration("check-timeout", 3*time.Second, "per-proxy dial/handshake timeout")
		checkURL       = flag.String("check-url", "https://aws.amazon.com", "website URL tested through proxy")
		socksHandshake = flag.Bool("socks5-handshake", true, "verify SOCKS5 greeting/handshake")
		gui            = flag.Bool("gui", false, "start web GUI")
		guiAddr        = flag.String("gui-addr", "127.0.0.1:8090", "web GUI listen address")
	)
	flag.Parse()
	changed := visitedFlags()
	cliGUI := *gui
	cliGUIAddr := *guiAddr

	cfg := appConfig{
		TargetURL: *targetURL,
		OutPath:   *outPath,
		CSVOut:    *csvOut,
		JSONOut:   *jsonOut,
		Timeout:   *timeout,
		Fetch: fetchConfig{
			FlareSolverrURL: *flareSolverrURL,
			FlareMaxTimeout: *flareTimeout,
			PageSize:        normalizePageSize(*pageSize),
			Retries:         maxInt(1, *retries),
			RetryBackoff:    *retryBackoff,
			RetryJitter:     *retryJitter,
		},
		Check: checkConfig{
			Enabled:        *checkAlive,
			OnlyAlive:      *onlyAlive,
			Workers:        maxInt(1, *checkWorkers),
			ConnectTimeout: *checkTimeout,
			SOCKS5:         *socksHandshake,
			TestURL:        strings.TrimSpace(*checkURL),
		},
	}

	if err := loadConfigIfAny(*configPath, &cfg, gui, guiAddr); err != nil {
		fmt.Fprintf(os.Stderr, "config load failed: %v\n", err)
		os.Exit(1)
	}
	if changed["gui"] {
		*gui = cliGUI
	}
	if changed["gui-addr"] {
		*guiAddr = cliGUIAddr
	}
	applyCliOverrides(changed, &cfg, targetURL, outPath, csvOut, jsonOut, timeout, flareSolverrURL, flareTimeout, retries, retryBackoff, retryJitter, checkAlive, onlyAlive, checkWorkers, checkTimeout, checkURL, socksHandshake, pageSize)

	if *gui {
		if err := startGUI(cfg, *guiAddr); err != nil {
			fmt.Fprintf(os.Stderr, "gui failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	summary, err := runScrape(cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	fmt.Println(summary)
}

func visitedFlags() map[string]bool {
	m := make(map[string]bool)
	flag.Visit(func(f *flag.Flag) {
		m[f.Name] = true
	})
	return m
}

func loadConfigIfAny(path string, cfg *appConfig, gui *bool, guiAddr *string) error {
	resolved, ok := resolveConfigPath(path)
	if !ok {
		return nil
	}
	b, err := os.ReadFile(resolved)
	if err != nil {
		return err
	}
	var fc fileConfig
	if err := json.Unmarshal(b, &fc); err != nil {
		return err
	}
	applyFileConfig(fc, cfg, gui, guiAddr)
	fmt.Printf("loaded config: %s\n", resolved)
	return nil
}

func resolveConfigPath(path string) (string, bool) {
	if strings.TrimSpace(path) != "" {
		return path, true
	}
	candidates := make([]string, 0, 2)
	if self, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "socks-grabber.json"))
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "socks-grabber.json"))
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

func applyFileConfig(fc fileConfig, cfg *appConfig, gui *bool, guiAddr *string) {
	if strings.TrimSpace(fc.URL) != "" {
		cfg.TargetURL = strings.TrimSpace(fc.URL)
	}
	if strings.TrimSpace(fc.Out) != "" {
		cfg.OutPath = strings.TrimSpace(fc.Out)
	}
	if strings.TrimSpace(fc.CSVOut) != "" {
		cfg.CSVOut = strings.TrimSpace(fc.CSVOut)
	}
	if fc.JSON != nil {
		cfg.JSONOut = *fc.JSON
	}
	if d, ok := parseDuration(fc.Timeout); ok {
		cfg.Timeout = d
	}
	if strings.TrimSpace(fc.FlareSolverrURL) != "" {
		cfg.Fetch.FlareSolverrURL = strings.TrimSpace(fc.FlareSolverrURL)
	}
	if d, ok := parseDuration(fc.FlareSolverrTimeout); ok {
		cfg.Fetch.FlareMaxTimeout = d
	}
	if fc.PageSize != nil {
		cfg.Fetch.PageSize = normalizePageSize(*fc.PageSize)
	}
	if fc.Retries != nil && *fc.Retries > 0 {
		cfg.Fetch.Retries = *fc.Retries
	}
	if d, ok := parseDuration(fc.RetryBackoff); ok {
		cfg.Fetch.RetryBackoff = d
	}
	if d, ok := parseDuration(fc.RetryJitter); ok {
		cfg.Fetch.RetryJitter = d
	}
	if fc.Check != nil {
		cfg.Check.Enabled = *fc.Check
	}
	if fc.OnlyAlive != nil {
		cfg.Check.OnlyAlive = *fc.OnlyAlive
	}
	if fc.CheckWorkers != nil && *fc.CheckWorkers > 0 {
		cfg.Check.Workers = *fc.CheckWorkers
	}
	if d, ok := parseDuration(fc.CheckTimeout); ok {
		cfg.Check.ConnectTimeout = d
	}
	if strings.TrimSpace(fc.CheckURL) != "" {
		cfg.Check.TestURL = strings.TrimSpace(fc.CheckURL)
	}
	if fc.SOCKS5Handshake != nil {
		cfg.Check.SOCKS5 = *fc.SOCKS5Handshake
	}
	if fc.GUI != nil {
		*gui = *fc.GUI
	}
	if strings.TrimSpace(fc.GUIAddr) != "" {
		*guiAddr = strings.TrimSpace(fc.GUIAddr)
	}
}

func parseDuration(v string) (time.Duration, bool) {
	v = strings.TrimSpace(v)
	if v == "" {
		return 0, false
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		return 0, false
	}
	return d, true
}

func applyCliOverrides(changed map[string]bool, cfg *appConfig, targetURL, outPath, csvOut *string, jsonOut *bool, timeout *time.Duration, flareSolverrURL *string, flareTimeout *time.Duration, retries *int, retryBackoff, retryJitter *time.Duration, checkAlive, onlyAlive *bool, checkWorkers *int, checkTimeout *time.Duration, checkURL *string, socksHandshake *bool, pageSize *int) {
	if changed["url"] {
		cfg.TargetURL = *targetURL
	}
	if changed["out"] {
		cfg.OutPath = *outPath
	}
	if changed["csv-out"] {
		cfg.CSVOut = *csvOut
	}
	if changed["json"] {
		cfg.JSONOut = *jsonOut
	}
	if changed["timeout"] {
		cfg.Timeout = *timeout
	}
	if changed["flaresolverr-url"] {
		cfg.Fetch.FlareSolverrURL = *flareSolverrURL
	}
	if changed["flaresolverr-timeout"] {
		cfg.Fetch.FlareMaxTimeout = *flareTimeout
	}
	if changed["page-size"] {
		cfg.Fetch.PageSize = normalizePageSize(*pageSize)
	}
	if changed["retries"] {
		cfg.Fetch.Retries = maxInt(1, *retries)
	}
	if changed["retry-backoff"] {
		cfg.Fetch.RetryBackoff = *retryBackoff
	}
	if changed["retry-jitter"] {
		cfg.Fetch.RetryJitter = *retryJitter
	}
	if changed["check"] {
		cfg.Check.Enabled = *checkAlive
	}
	if changed["only-alive"] {
		cfg.Check.OnlyAlive = *onlyAlive
	}
	if changed["check-workers"] {
		cfg.Check.Workers = maxInt(1, *checkWorkers)
	}
	if changed["check-timeout"] {
		cfg.Check.ConnectTimeout = *checkTimeout
	}
	if changed["check-url"] {
		cfg.Check.TestURL = strings.TrimSpace(*checkURL)
	}
	if changed["socks5-handshake"] {
		cfg.Check.SOCKS5 = *socksHandshake
	}
}

func runScrape(cfg appConfig) (string, error) {
	if err := ensureFlareSolverrReady(cfg.Fetch.FlareSolverrURL); err != nil {
		return "", err
	}

	effectiveTimeout := maxDuration(cfg.Timeout, estimateRuntimeBudget(cfg))
	ctx, cancel := context.WithTimeout(context.Background(), effectiveTimeout)
	defer cancel()

	htmlDoc, source, err := fetchWithRetry(ctx, cfg.TargetURL, cfg.Fetch)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}

	allProxies := extractProxies(htmlDoc, source)
	if len(allProxies) == 0 {
		return "", errors.New("no proxies extracted; page format changed or source is empty")
	}

	if cfg.Check.Enabled {
		allProxies = checkProxies(ctx, allProxies, cfg.Check)
	} else {
		for i := range allProxies {
			allProxies[i].Error = "not_checked"
		}
	}

	if err := writeCSVOutput(cfg.CSVOut, allProxies); err != nil {
		return "", fmt.Errorf("write csv failed: %w", err)
	}

	aliveProxies := filterAlive(allProxies)
	if len(aliveProxies) == 0 {
		return "", fmt.Errorf("no alive proxies after checks; csv exported to %s", cfg.CSVOut)
	}

	if err := writeOutput(cfg.OutPath, aliveProxies, cfg.JSONOut); err != nil {
		return "", fmt.Errorf("write output failed: %w", err)
	}

	return fmt.Sprintf("ok: alive %d / total %d (%s) -> txt:%s csv:%s", len(aliveProxies), len(allProxies), source, cfg.OutPath, cfg.CSVOut), nil
}

func filterAlive(proxies []Proxy) []Proxy {
	out := make([]Proxy, 0, len(proxies))
	for _, p := range proxies {
		if p.Alive {
			out = append(out, p)
		}
	}
	return out
}

func estimateRuntimeBudget(cfg appConfig) time.Duration {
	if cfg.Fetch.Retries < 1 {
		cfg.Fetch.Retries = 1
	}
	fetchBudget := 0 * time.Second
	for i := 1; i <= cfg.Fetch.Retries; i++ {
		fetchBudget += cfg.Fetch.FlareMaxTimeout
		if i < cfg.Fetch.Retries {
			d := cfg.Fetch.RetryBackoff * time.Duration(1<<(i-1))
			if d > 45*time.Second {
				d = 45 * time.Second
			}
			d += cfg.Fetch.RetryJitter
			fetchBudget += d
		}
	}
	fetchBudget += 30 * time.Second

	if !cfg.Check.Enabled {
		return fetchBudget
	}

	workers := maxInt(1, cfg.Check.Workers)
	pageSize := maxInt(1, cfg.Fetch.PageSize)
	probeUnit := cfg.Check.ConnectTimeout
	if cfg.Check.SOCKS5 {
		probeUnit += cfg.Check.ConnectTimeout
	}
	if strings.TrimSpace(cfg.Check.TestURL) != "" {
		probeUnit += cfg.Check.ConnectTimeout * 2
	}
	checkBudget := time.Duration((pageSize+workers-1)/workers) * probeUnit
	checkBudget += 15 * time.Second
	return fetchBudget + checkBudget
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}

func ensureFlareSolverrReady(endpoint string) error {
	if flaresolverrReachable(endpoint, 1500*time.Millisecond) {
		return nil
	}

	exePath, err := findLocalFlareSolverrExe()
	if err != nil {
		if derr := downloadFlareSolverrIfNeeded(); derr != nil {
			return fmt.Errorf("flaresolverr not running and local executable not found: %v; auto-download failed: %w", err, derr)
		}
		exePath, err = findLocalFlareSolverrExe()
		if err != nil {
			return fmt.Errorf("flaresolverr not running and local executable still missing after download: %w", err)
		}
	}

	cmd := exec.Command(exePath)
	cmd.Dir = filepath.Dir(exePath)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if runtime.GOOS == "windows" {
		cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", exePath, err)
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}

	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		if flaresolverrReachable(endpoint, 1500*time.Millisecond) {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	return fmt.Errorf("flaresolverr did not become ready at %s", endpoint)
}

type ghRelease struct {
	TagName string `json:"tag_name"`
	Assets  []struct {
		Name               string `json:"name"`
		BrowserDownloadURL string `json:"browser_download_url"`
	} `json:"assets"`
}

func downloadFlareSolverrIfNeeded() error {
	base, err := resolveToolBaseDir()
	if err != nil {
		return err
	}
	targetDir := filepath.Join(base, "flaresolverr")
	if st, err := os.Stat(targetDir); err == nil && st.IsDir() {
		if _, e := findLocalFlareSolverrExe(); e == nil {
			return nil
		}
	}

	assetURL, assetName, err := resolveFlareSolverrAsset()
	if err != nil {
		return err
	}

	tmpFile := filepath.Join(base, "flaresolverr_download.tmp")
	if err := downloadFile(assetURL, tmpFile); err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmpFile) }()

	tmpExtractDir := filepath.Join(base, "flaresolverr_extract_tmp")
	_ = os.RemoveAll(tmpExtractDir)
	if err := os.MkdirAll(tmpExtractDir, 0o755); err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpExtractDir) }()

	if strings.HasSuffix(strings.ToLower(assetName), ".zip") {
		if err := extractZip(tmpFile, tmpExtractDir); err != nil {
			return err
		}
	} else if strings.HasSuffix(strings.ToLower(assetName), ".tar.gz") {
		if err := extractTarGz(tmpFile, tmpExtractDir); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unsupported FlareSolverr archive: %s", assetName)
	}

	extractedRoot, err := locateExtractedFlareSolverrDir(tmpExtractDir)
	if err != nil {
		return err
	}

	_ = os.RemoveAll(targetDir)
	if err := os.Rename(extractedRoot, targetDir); err != nil {
		return err
	}
	return nil
}

func resolveToolBaseDir() (string, error) {
	if self, err := os.Executable(); err == nil && strings.TrimSpace(self) != "" {
		return filepath.Dir(self), nil
	}
	if wd, err := os.Getwd(); err == nil && strings.TrimSpace(wd) != "" {
		return wd, nil
	}
	return "", errors.New("unable to resolve tool base directory")
}

func resolveFlareSolverrAsset() (url, name string, err error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.github.com/repos/FlareSolverr/FlareSolverr/releases/latest", nil)
	if err != nil {
		return "", "", err
	}
	req.Header.Set("User-Agent", "socks-grabber")

	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return "", "", fmt.Errorf("github api status=%d", resp.StatusCode)
	}

	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return "", "", err
	}

	want := ""
	if runtime.GOOS == "windows" {
		want = "windows_x64.zip"
	} else if runtime.GOOS == "linux" {
		want = "linux_x64.tar.gz"
	} else {
		return "", "", fmt.Errorf("unsupported OS for auto-download: %s", runtime.GOOS)
	}

	for _, a := range rel.Assets {
		if strings.Contains(strings.ToLower(a.Name), want) {
			return a.BrowserDownloadURL, a.Name, nil
		}
	}
	return "", "", fmt.Errorf("no matching FlareSolverr asset found for %s", runtime.GOOS)
}

func downloadFile(srcURL, dstPath string) error {
	req, err := http.NewRequest(http.MethodGet, srcURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "socks-grabber")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("download failed status=%d", resp.StatusCode)
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}

func extractZip(archivePath, destDir string) error {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer zr.Close()

	for _, f := range zr.File {
		target := filepath.Join(destDir, f.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe zip path: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(target)
		if err != nil {
			_ = rc.Close()
			return err
		}
		_, cErr := io.Copy(out, rc)
		_ = out.Close()
		_ = rc.Close()
		if cErr != nil {
			return cErr
		}
	}
	return nil
}

func extractTarGz(archivePath, destDir string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gzr, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gzr.Close()

	tr := tar.NewReader(gzr)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		target := filepath.Join(destDir, hdr.Name)
		if !strings.HasPrefix(filepath.Clean(target), filepath.Clean(destDir)+string(os.PathSeparator)) {
			return fmt.Errorf("unsafe tar path: %s", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.Create(target)
			if err != nil {
				return err
			}
			_, cErr := io.Copy(out, tr)
			_ = out.Close()
			if cErr != nil {
				return cErr
			}
		default:
			// ignore non-regular entries
		}
	}
	return nil
}

func locateExtractedFlareSolverrDir(root string) (string, error) {
	exeName := "flaresolverr"
	if runtime.GOOS == "windows" {
		exeName = "flaresolverr.exe"
	}

	var best string
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.EqualFold(d.Name(), exeName) {
			best = filepath.Dir(path)
			return io.EOF
		}
		return nil
	})
	if err != nil && err != io.EOF {
		return "", err
	}
	if best == "" {
		return "", errors.New("downloaded archive does not contain flaresolverr executable")
	}
	return best, nil
}

func findLocalFlareSolverrExe() (string, error) {
	exeName := "flaresolverr.exe"
	if runtime.GOOS != "windows" {
		exeName = "flaresolverr"
	}
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	candidates := make([]string, 0, 2)
	if self != "" {
		candidates = append(candidates, filepath.Join(filepath.Dir(self), "flaresolverr", exeName))
	}
	if wd, e := os.Getwd(); e == nil {
		candidates = append(candidates, filepath.Join(wd, "flaresolverr", exeName))
	}
	for _, candidate := range candidates {
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("unable to resolve executable directory")
	}
	return "", fmt.Errorf("expected %s", strings.Join(candidates, " or "))
}

func flaresolverrReachable(endpoint string, timeout time.Duration) bool {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" {
		return false
	}
	host := u.Host
	if !strings.Contains(host, ":") {
		if strings.EqualFold(u.Scheme, "https") {
			host += ":443"
		} else {
			host += ":80"
		}
	}
	conn, err := net.DialTimeout("tcp", host, timeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func startGUI(defaultCfg appConfig, addr string) error {
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		cfg := defaultCfg
		msg := ""
		errMsg := ""
		if r.Method == http.MethodPost {
			if err := r.ParseForm(); err != nil {
				errMsg = err.Error()
			} else {
				cfg.TargetURL = strings.TrimSpace(r.FormValue("url"))
				cfg.OutPath = strings.TrimSpace(r.FormValue("out"))
				if v := strings.TrimSpace(r.FormValue("csv_out")); v != "" {
					cfg.CSVOut = v
				}
				cfg.JSONOut = r.FormValue("json") == "on"
				cfg.Fetch.FlareSolverrURL = strings.TrimSpace(r.FormValue("flaresolverr_url"))
				cfg.Check.Enabled = r.FormValue("check") == "on"
				cfg.Check.OnlyAlive = r.FormValue("only_alive") == "on"
				if v := strings.TrimSpace(r.FormValue("check_url")); v != "" {
					cfg.Check.TestURL = v
				}

				if v, e := strconv.Atoi(strings.TrimSpace(r.FormValue("retries"))); e == nil && v > 0 {
					cfg.Fetch.Retries = v
				}
				if v, e := strconv.Atoi(strings.TrimSpace(r.FormValue("page_size"))); e == nil && v > 0 {
					cfg.Fetch.PageSize = normalizePageSize(v)
				}
				if d, e := time.ParseDuration(strings.TrimSpace(r.FormValue("timeout"))); e == nil && d > 0 {
					cfg.Timeout = d
				}
				if d, e := time.ParseDuration(strings.TrimSpace(r.FormValue("flaresolverr_timeout"))); e == nil && d > 0 {
					cfg.Fetch.FlareMaxTimeout = d
				}
				if v, e := strconv.Atoi(strings.TrimSpace(r.FormValue("check_workers"))); e == nil && v > 0 {
					cfg.Check.Workers = v
				}
				if d, e := time.ParseDuration(strings.TrimSpace(r.FormValue("check_timeout"))); e == nil && d > 0 {
					cfg.Check.ConnectTimeout = d
				}

				s, err := runScrape(cfg)
				if err != nil {
					errMsg = err.Error()
				} else {
					msg = s
				}
			}
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><title>SOCKS Grabber GUI</title><style>body{font-family:Segoe UI,Arial,sans-serif;background:#0f172a;color:#e2e8f0;max-width:920px;margin:24px auto;padding:0 16px}input[type=text]{width:100%%;padding:8px;border:1px solid #334155;background:#111827;color:#e2e8f0;border-radius:6px}label{display:block;margin:10px 0 6px}button{margin-top:14px;padding:10px 16px;background:#2563eb;color:#fff;border:0;border-radius:6px;cursor:pointer}.row{display:grid;grid-template-columns:1fr 1fr;gap:12px}.box{background:#111827;padding:14px;border-radius:8px;border:1px solid #1f2937}.ok{color:#22c55e}.err{color:#f87171}</style></head><body><h2>SOCKS Grabber GUI</h2><form method="post"><div class="box"><label>URL</label><input name="url" type="text" value="%s"><label>TXT Output (alive only)</label><input name="out" type="text" value="%s"><label>CSV Output (all results)</label><input name="csv_out" type="text" value="%s"><div class="row"><div><label>Timeout</label><input name="timeout" type="text" value="%s"></div><div><label>Retries</label><input name="retries" type="text" value="%d"></div></div><div class="row"><div><label>Page Size (30/50/100/200/300/500)</label><input name="page_size" type="text" value="%d"></div><div><label>FlareSolverr URL</label><input name="flaresolverr_url" type="text" value="%s"></div></div><div class="row"><div><label>FlareSolverr Timeout</label><input name="flaresolverr_timeout" type="text" value="%s"></div><div><label>Check Workers</label><input name="check_workers" type="text" value="%d"></div></div><div class="row"><div><label>Check Timeout</label><input name="check_timeout" type="text" value="%s"></div><div><label>Check URL</label><input name="check_url" type="text" value="%s"></div></div><label><input type="checkbox" name="json" %s> JSON output</label><label><input type="checkbox" name="check" %s> Availability check</label><label><input type="checkbox" name="only_alive" %s> Only alive</label><button type="submit">Run</button></div></form><p class="ok">%s</p><p class="err">%s</p></body></html>`,
			html.EscapeString(cfg.TargetURL),
			html.EscapeString(cfg.OutPath),
			html.EscapeString(cfg.CSVOut),
			html.EscapeString(cfg.Timeout.String()),
			cfg.Fetch.Retries,
			cfg.Fetch.PageSize,
			html.EscapeString(cfg.Fetch.FlareSolverrURL),
			html.EscapeString(cfg.Fetch.FlareMaxTimeout.String()),
			cfg.Check.Workers,
			html.EscapeString(cfg.Check.ConnectTimeout.String()),
			html.EscapeString(cfg.Check.TestURL),
			checked(cfg.JSONOut),
			checked(cfg.Check.Enabled),
			checked(cfg.Check.OnlyAlive),
			html.EscapeString(msg),
			html.EscapeString(errMsg),
		)
	})

	fmt.Printf("GUI running: http://%s\n", addr)
	return http.ListenAndServe(addr, nil)
}

func checked(v bool) string {
	if v {
		return "checked"
	}
	return ""
}

func fetchWithRetry(ctx context.Context, targetURL string, cfg fetchConfig) (string, string, error) {
	var lastErr error
	for attempt := 1; attempt <= cfg.Retries; attempt++ {
		if htmlDoc, err := fetchFlareSolverr(ctx, targetURL, cfg); err == nil {
			return htmlDoc, "flaresolverr", nil
		} else {
			lastErr = fmt.Errorf("attempt %d flaresolverr failed: %w", attempt, err)
		}

		if attempt < cfg.Retries {
			if err := sleepBackoff(ctx, attempt, cfg.RetryBackoff, cfg.RetryJitter); err != nil {
				return "", "", err
			}
		}
	}

	if lastErr == nil {
		lastErr = errors.New("flaresolverr fetch failed with unknown reason")
	}
	return "", "", lastErr
}

func sleepBackoff(ctx context.Context, attempt int, base, jitter time.Duration) error {
	if base <= 0 {
		base = time.Second
	}
	d := base * time.Duration(1<<(attempt-1))
	if d > 45*time.Second {
		d = 45 * time.Second
	}
	if jitter > 0 {
		d += time.Duration(rng.Int63n(int64(jitter)))
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

type flareSolverrRequest struct {
	Cmd        string `json:"cmd"`
	URL        string `json:"url"`
	MaxTimeout int64  `json:"maxTimeout,omitempty"`
	PostData   string `json:"postData,omitempty"`
}

type flareSolverrResponse struct {
	Status   string `json:"status"`
	Message  string `json:"message"`
	Solution struct {
		Status   int    `json:"status"`
		Response string `json:"response"`
	} `json:"solution"`
}

func fetchFlareSolverr(ctx context.Context, targetURL string, cfg fetchConfig) (string, error) {
	pageSize := normalizePageSize(cfg.PageSize)
	if pageSize == 30 {
		return callFlareSolverr(ctx, cfg.FlareSolverrURL, flareSolverrRequest{Cmd: "request.get", URL: targetURL, MaxTimeout: cfg.FlareMaxTimeout.Milliseconds()}, cfg.FlareMaxTimeout)
	}

	firstHTML, err := callFlareSolverr(ctx, cfg.FlareSolverrURL, flareSolverrRequest{Cmd: "request.get", URL: targetURL, MaxTimeout: cfg.FlareMaxTimeout.Milliseconds()}, cfg.FlareMaxTimeout)
	if err != nil {
		return "", err
	}

	token := extractXX0Token(firstHTML)
	if token == "" {
		return firstHTML, nil
	}

	xppValue := pageSizeOptionValue(pageSize)

	postData := "xx0=" + url.QueryEscape(token) + "&xpp=" + xppValue
	secondReq := flareSolverrRequest{Cmd: "request.post", URL: targetURL, MaxTimeout: cfg.FlareMaxTimeout.Milliseconds(), PostData: postData}
	secondHTML, err := callFlareSolverr(ctx, cfg.FlareSolverrURL, secondReq, cfg.FlareMaxTimeout)
	if err != nil {
		return "", err
	}
	if len(extractProxies(secondHTML, "flaresolverr")) == 0 {
		return firstHTML, nil
	}
	return secondHTML, nil
}

func callFlareSolverr(ctx context.Context, endpoint string, payload flareSolverrRequest, maxTimeout time.Duration) (string, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(b))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	clientTimeout := maxTimeout + 20*time.Second
	if clientTimeout < 30*time.Second {
		clientTimeout = 30 * time.Second
	}
	client := &http.Client{Timeout: clientTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	raw, err := ioReadAllLimited(resp.Body, 16<<20)
	if err != nil {
		return "", err
	}
	var out flareSolverrResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("invalid flaresolverr response: %w", err)
	}
	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("flaresolverr http %d: %s", resp.StatusCode, out.Message)
	}
	if strings.ToLower(out.Status) != "ok" {
		return "", fmt.Errorf("flaresolverr status=%s message=%s", out.Status, out.Message)
	}
	if out.Solution.Response == "" {
		return "", errors.New("flaresolverr returned empty solution.response")
	}
	return out.Solution.Response, nil
}

func extractXX0Token(htmlDoc string) string {
	m := regexp.MustCompile(`name=["']xx0["']\s+value=["']([^"']+)`).FindStringSubmatch(htmlDoc)
	if len(m) == 2 {
		return m[1]
	}
	return ""
}

func extractProxies(html, source string) []Proxy {
	seen := make(map[string]struct{})
	out := make([]Proxy, 0, 256)
	add := func(ip, portStr, protocol string) {
		port, err := strconv.Atoi(portStr)
		if err != nil || port < 1 || port > 65535 || !validIPv4(ip) {
			return
		}
		addr := ip + ":" + strconv.Itoa(port)
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		out = append(out, Proxy{Addr: addr, Protocol: normalizeProtocol(protocol), IP: ip, Port: port, Source: source})
	}

	for _, m := range htmlCellRE.FindAllStringSubmatch(html, -1) {
		add(m[1], m[2], m[3])
	}

	for _, m := range ipPortRE.FindAllStringSubmatch(html, -1) {
		add(m[1], m[2], "socks5")
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].IP == out[j].IP {
			return out[i].Port < out[j].Port
		}
		return out[i].IP < out[j].IP
	})
	return out
}

func normalizeProtocol(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	if s == "" {
		return "socks5"
	}
	if strings.HasPrefix(s, "socks") {
		return s
	}
	if s == "http" || s == "https" {
		return s
	}
	return "socks5"
}

func checkProxies(ctx context.Context, proxies []Proxy, cfg checkConfig) []Proxy {
	type result struct {
		idx int
		p   Proxy
	}

	jobs := make(chan int)
	results := make(chan result, len(proxies))
	var wg sync.WaitGroup

	for i := 0; i < cfg.Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for idx := range jobs {
				p := proxies[idx]
				lat, err := probeProxy(ctx, p.Addr, cfg)
				if err != nil {
					p.Alive = false
					p.Error = err.Error()
				} else {
					p.Alive = true
					p.LatencyMS = lat.Milliseconds()
				}
				results <- result{idx: idx, p: p}
			}
		}()
	}

	go func() {
		for i := range proxies {
			select {
			case <-ctx.Done():
				close(jobs)
				wg.Wait()
				close(results)
				return
			case jobs <- i:
			}
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	out := make([]Proxy, 0, len(proxies))
	for r := range results {
		_ = r.idx
		out = append(out, r.p)
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Alive != out[j].Alive {
			return out[i].Alive
		}
		if out[i].LatencyMS == out[j].LatencyMS {
			return out[i].Addr < out[j].Addr
		}
		if out[i].LatencyMS == 0 {
			return false
		}
		if out[j].LatencyMS == 0 {
			return true
		}
		return out[i].LatencyMS < out[j].LatencyMS
	})
	return out
}

func probeProxy(ctx context.Context, addr string, cfg checkConfig) (time.Duration, error) {
	start := time.Now()
	if err := checkTCP(ctx, addr, cfg.ConnectTimeout); err != nil {
		return 0, err
	}
	if cfg.SOCKS5 {
		if err := checkSOCKS5Handshake(ctx, addr, cfg.ConnectTimeout); err != nil {
			return 0, err
		}
	}
	if strings.TrimSpace(cfg.TestURL) != "" {
		if err := checkHTTPViaSOCKS(ctx, addr, cfg.TestURL, cfg.ConnectTimeout); err != nil {
			return 0, err
		}
	}
	return time.Since(start), nil
}

func checkHTTPViaSOCKS(ctx context.Context, proxyAddr, testURL string, timeout time.Duration) error {
	baseDialer := &net.Dialer{Timeout: timeout}
	socksDialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, baseDialer)
	if err != nil {
		return fmt.Errorf("socks dialer failed: %w", err)
	}

	transport := &http.Transport{
		DialContext: func(reqCtx context.Context, network, addr string) (net.Conn, error) {
			return socksDialer.Dial(network, addr)
		},
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		ExpectContinueTimeout: 1 * time.Second,
		DisableKeepAlives:     true,
	}
	client := &http.Client{Transport: transport}

	reqCtx, cancel := context.WithTimeout(ctx, timeout*2)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, testURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "socks-grabber/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("http via proxy failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return fmt.Errorf("http via proxy status=%d", resp.StatusCode)
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1024))
	return nil
}

func checkTCP(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("tcp failed: %w", err)
	}
	_ = conn.Close()
	return nil
}

func checkSOCKS5Handshake(ctx context.Context, addr string, timeout time.Duration) error {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return fmt.Errorf("socks dial failed: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		return fmt.Errorf("socks write greeting failed: %w", err)
	}
	resp := make([]byte, 2)
	if _, err := io.ReadFull(conn, resp); err != nil {
		return fmt.Errorf("socks read greeting failed: %w", err)
	}
	if resp[0] != 0x05 {
		return fmt.Errorf("invalid socks version: %d", resp[0])
	}
	if resp[1] == 0xFF {
		return errors.New("socks no acceptable auth method")
	}
	if resp[1] != 0x00 {
		return fmt.Errorf("socks unsupported auth method: 0x%02x", resp[1])
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	return nil
}

func validIPv4(ip string) bool {
	parts := strings.Split(ip, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || n > 255 {
			return false
		}
	}
	return true
}

func writeOutput(path string, proxies []Proxy, asJSON bool) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	if asJSON {
		enc := json.NewEncoder(f)
		enc.SetIndent("", "  ")
		return enc.Encode(proxies)
	}

	for _, p := range proxies {
		if _, err := fmt.Fprintln(f, p.Protocol+"://"+p.Addr); err != nil {
			return err
		}
	}
	return nil
}

func writeCSVOutput(path string, proxies []Proxy) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	if err := w.Write([]string{"protocol", "ip", "port", "proxy", "alive", "latency_ms", "error", "source"}); err != nil {
		return err
	}
	for _, p := range proxies {
		rec := []string{
			p.Protocol,
			p.IP,
			strconv.Itoa(p.Port),
			p.Protocol + "://" + p.Addr,
			strconv.FormatBool(p.Alive),
			strconv.FormatInt(p.LatencyMS, 10),
			p.Error,
			p.Source,
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	return w.Error()
}

func ioReadAllLimited(r io.Reader, max int64) ([]byte, error) {
	lr := &io.LimitedReader{R: r, N: max}
	b, err := io.ReadAll(lr)
	if err != nil {
		return nil, err
	}
	return b, nil
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func normalizePageSize(v int) int {
	allowed := []int{30, 50, 100, 200, 300, 500}
	if v <= allowed[0] {
		return allowed[0]
	}
	if v >= allowed[len(allowed)-1] {
		return allowed[len(allowed)-1]
	}
	best := allowed[0]
	bestDiff := absInt(v - best)
	for _, a := range allowed[1:] {
		d := absInt(v - a)
		if d < bestDiff {
			best = a
			bestDiff = d
		}
	}
	return best
}

func pageSizeOptionValue(pageSize int) string {
	switch normalizePageSize(pageSize) {
	case 30:
		return "0"
	case 50:
		return "1"
	case 100:
		return "2"
	case 200:
		return "3"
	case 300:
		return "4"
	default:
		return "5"
	}
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}
