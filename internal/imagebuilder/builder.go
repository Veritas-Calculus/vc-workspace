package imagebuilder

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

type Config struct {
	Enabled            bool
	PackerPath         string
	RootDir            string
	PVEEndpoint        string
	PVEUsername        string
	PVEPassword        string
	PVETokenID         string
	PVETokenSecret     string
	LinuxAgentBinary   string
	WindowsAgentBinary string
	CloudbaseInitMSI   string
}

type Request struct {
	ProfileID         string
	Node              string
	VMID              int
	SourceISO         string
	SourceISOChecksum string
	DriverISO         string
	DriverISOChecksum string
	MirrorURL         string
	SecurityMirrorURL string
	StoragePool       string
	Bridge            string
	WindowsImageName  string
	Cores             int
	MemoryMB          int
	DiskGB            int
	Firmware          string
	TPMVersion        string
	BuilderPassword   string
	WindowsProductKey string
}

type Progress struct {
	Percent int
	Detail  string
}

type definition struct {
	templatePath string
	templateName string
	windows      bool
	pveOSType    string
}

var definitions = map[string]definition{
	"debian-13-xfce": {
		templatePath: "debian-13-xfce/debian-13-xfce.pkr.hcl",
		templateName: "vc-vdi-debian-13-xfce",
	},
	"windows-10-22h2": {
		templatePath: "windows-client/windows-client.pkr.hcl",
		templateName: "vc-vdi-windows-10-22h2",
		windows:      true,
		pveOSType:    "win10",
	},
	"windows-11": {
		templatePath: "windows-client/windows-client.pkr.hcl",
		templateName: "vc-vdi-windows-11",
		windows:      true,
		pveOSType:    "win11",
	},
}

type Builder struct {
	config     Config
	packerPath string
	rootDir    string
}

func New(config Config) (*Builder, error) {
	builder := &Builder{config: config}
	if !config.Enabled {
		return builder, nil
	}
	packerPath := strings.TrimSpace(config.PackerPath)
	if packerPath == "" {
		packerPath = "packer"
	}
	resolvedPacker, err := exec.LookPath(packerPath)
	if err != nil {
		return nil, errors.New("packer executable was not found")
	}
	rootDir, err := filepath.Abs(config.RootDir)
	if err != nil {
		return nil, fmt.Errorf("resolve image root: %w", err)
	}
	artifactPaths := []*string{&config.LinuxAgentBinary, &config.WindowsAgentBinary, &config.CloudbaseInitMSI}
	for _, path := range artifactPaths {
		absolute, absoluteErr := filepath.Abs(*path)
		if absoluteErr != nil {
			return nil, fmt.Errorf("resolve image builder artifact: %w", absoluteErr)
		}
		*path = absolute
	}
	for _, path := range []string{
		filepath.Join(rootDir, definitions["debian-13-xfce"].templatePath),
		filepath.Join(rootDir, definitions["windows-11"].templatePath),
		config.LinuxAgentBinary,
		config.WindowsAgentBinary,
		config.CloudbaseInitMSI,
	} {
		if info, statErr := os.Stat(path); statErr != nil || info.IsDir() {
			return nil, fmt.Errorf("required image builder artifact is unavailable: %s", filepath.Base(path))
		}
	}
	parsed, err := url.Parse(config.PVEEndpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("image builder PVE endpoint must be an absolute HTTPS URL")
	}
	passwordAuth := config.PVEUsername != "" && config.PVEPassword != ""
	tokenAuth := config.PVETokenID != "" && config.PVETokenSecret != ""
	if passwordAuth == tokenAuth {
		return nil, errors.New("image builder requires exactly one PVE authentication method")
	}
	builder.packerPath = resolvedPacker
	builder.rootDir = rootDir
	builder.config = config
	return builder, nil
}

func (b *Builder) Available() bool {
	return b != nil && b.config.Enabled && b.packerPath != ""
}

func (b *Builder) Build(ctx context.Context, request Request, report func(Progress)) error {
	if !b.Available() {
		return errors.New("image builder is not configured")
	}
	if err := ValidateRequest(request); err != nil {
		return err
	}
	spec, err := b.commandSpec(request)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, b.packerPath, spec.args...)
	cmd.Dir = spec.dir
	cmd.Env = append(os.Environ(), spec.env...)
	writer := &progressWriter{report: report, secrets: spec.secrets}
	cmd.Stdout = writer
	cmd.Stderr = writer
	if report != nil {
		report(Progress{Percent: 2, Detail: "Starting Packer"})
	}
	if err := cmd.Run(); err != nil {
		last := writer.Last()
		if last == "" {
			last = "Packer exited before reporting a result"
		}
		return fmt.Errorf("image build failed: %s", last)
	}
	if report != nil {
		report(Progress{Percent: 100, Detail: "Packer created the PVE template"})
	}
	return nil
}

type commandSpec struct {
	dir     string
	args    []string
	env     []string
	secrets []string
}

func (b *Builder) commandSpec(request Request) (commandSpec, error) {
	definition, ok := definitions[request.ProfileID]
	if !ok {
		return commandSpec{}, errors.New("unsupported image profile")
	}
	pveURL := strings.TrimRight(b.config.PVEEndpoint, "/") + "/api2/json"
	username, password, token := b.config.PVEUsername, b.config.PVEPassword, ""
	if b.config.PVETokenID != "" {
		username, token = b.config.PVETokenID, b.config.PVETokenSecret
	}
	values := map[string]string{
		"pve_url":       pveURL,
		"pve_username":  username,
		"pve_password":  password,
		"pve_token":     token,
		"pve_node":      request.Node,
		"vm_id":         strconv.Itoa(request.VMID),
		"storage_pool":  request.StoragePool,
		"bridge":        request.Bridge,
		"cores":         strconv.Itoa(request.Cores),
		"memory_mb":     strconv.Itoa(request.MemoryMB),
		"disk_size":     fmt.Sprintf("%dG", request.DiskGB),
		"template_name": definition.templateName,
	}
	if definition.windows {
		values["windows_iso_file"] = request.SourceISO
		values["windows_iso_checksum"] = request.SourceISOChecksum
		values["virtio_iso_file"] = request.DriverISO
		values["virtio_iso_checksum"] = request.DriverISOChecksum
		values["windows_image_name"] = request.WindowsImageName
		values["pve_os_type"] = definition.pveOSType
		values["administrator_password"] = request.BuilderPassword
		values["product_key"] = request.WindowsProductKey
		values["agent_binary"] = b.config.WindowsAgentBinary
		values["cloudbase_init_msi"] = b.config.CloudbaseInitMSI
	} else {
		mirror, _ := url.Parse(request.MirrorURL)
		values["iso_file"] = request.SourceISO
		values["iso_checksum"] = request.SourceISOChecksum
		values["mirror_host"] = mirror.Hostname()
		values["mirror_url"] = request.MirrorURL
		values["security_mirror_url"] = request.SecurityMirrorURL
		values["builder_password"] = request.BuilderPassword
		values["firmware"] = map[string]string{"seabios": "seabios", "uefi": "ovmf"}[request.Firmware]
		values["agent_binary"] = b.config.LinuxAgentBinary
	}
	env := make([]string, 0, len(values))
	for key, value := range values {
		env = append(env, "PKR_VAR_"+key+"="+value)
	}
	return commandSpec{
		dir:     filepath.Dir(filepath.Join(b.rootDir, definition.templatePath)),
		args:    []string{"build", "-color=false", filepath.Join(b.rootDir, definition.templatePath)},
		env:     env,
		secrets: []string{password, token, request.BuilderPassword, request.WindowsProductKey},
	}, nil
}

var (
	namePattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)
	mediaPattern    = regexp.MustCompile(`^[A-Za-z0-9._-]+:iso/[A-Za-z0-9][A-Za-z0-9._+() -]{0,220}$`)
	checksumPattern = regexp.MustCompile(`^sha256:[0-9a-fA-F]{64}$`)
	passwordPattern = regexp.MustCompile(`^[A-Za-z0-9!@#$%^*()_+={}\[\]:,.?-]{12,128}$`)
	productPattern  = regexp.MustCompile(`^[A-Za-z0-9-]{0,100}$`)
)

func ValidateRequest(request Request) error {
	definition, ok := definitions[request.ProfileID]
	if !ok {
		return errors.New("unsupported image profile")
	}
	if !namePattern.MatchString(request.Node) || !namePattern.MatchString(request.StoragePool) || !namePattern.MatchString(request.Bridge) {
		return errors.New("PVE node, storage, or bridge is invalid")
	}
	if request.VMID < 100 || request.VMID > 999999999 {
		return errors.New("template VMID must be between 100 and 999999999")
	}
	if !mediaPattern.MatchString(request.SourceISO) || !checksumPattern.MatchString(request.SourceISOChecksum) {
		return errors.New("OS ISO and SHA-256 checksum are required")
	}
	if request.Cores < 1 || request.Cores > 256 || request.MemoryMB < 512 || request.MemoryMB > 1048576 || request.DiskGB < 8 || request.DiskGB > 16384 {
		return errors.New("image hardware values are outside supported limits")
	}
	if !passwordPattern.MatchString(request.BuilderPassword) {
		return errors.New("build password must be 12–128 characters using letters, numbers, or safe punctuation")
	}
	if definition.windows {
		if !mediaPattern.MatchString(request.DriverISO) || !checksumPattern.MatchString(request.DriverISOChecksum) {
			return errors.New("Windows builds require a driver ISO and SHA-256 checksum")
		}
		if strings.TrimSpace(request.WindowsImageName) == "" || len(request.WindowsImageName) > 100 {
			return errors.New("Windows image name is required")
		}
		if !productPattern.MatchString(request.WindowsProductKey) {
			return errors.New("Windows product key contains unsupported characters")
		}
		if request.Firmware != "uefi" || request.TPMVersion != "2.0" {
			return errors.New("Windows templates require UEFI and TPM 2.0")
		}
		return nil
	}
	if request.DriverISO != "" || request.DriverISOChecksum != "" || request.WindowsImageName != "" || request.WindowsProductKey != "" {
		return errors.New("Linux builds cannot include Windows-only settings")
	}
	if request.Firmware != "seabios" && request.Firmware != "uefi" {
		return errors.New("Linux firmware must be seabios or uefi")
	}
	if request.TPMVersion != "none" {
		return errors.New("Debian 13 bootstrap does not currently support TPM")
	}
	for _, raw := range []string{request.MirrorURL, request.SecurityMirrorURL} {
		parsed, err := url.Parse(raw)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
			return errors.New("Debian package mirrors must be absolute HTTP(S) URLs without credentials")
		}
	}
	return nil
}

type progressWriter struct {
	mu      sync.Mutex
	pending string
	last    string
	percent int
	report  func(Progress)
	secrets []string
}

func (w *progressWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.pending += string(data)
	for {
		line, remainder, found := strings.Cut(w.pending, "\n")
		if !found {
			break
		}
		w.pending = remainder
		w.consume(line)
	}
	return len(data), nil
}

func (w *progressWriter) consume(line string) {
	line = strings.TrimSpace(stripANSI(line))
	for _, secret := range w.secrets {
		if secret != "" {
			line = strings.ReplaceAll(line, secret, "[redacted]")
		}
	}
	if line == "" {
		return
	}
	if len(line) > 480 {
		line = line[:480]
	}
	w.last = line
	w.percent = progressPercent(w.percent, line)
	if w.report != nil {
		w.report(Progress{Percent: w.percent, Detail: line})
	}
}

func (w *progressWriter) Last() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	if strings.TrimSpace(w.pending) != "" {
		w.consume(w.pending)
		w.pending = ""
	}
	return w.last
}

func progressPercent(current int, line string) int {
	lower := strings.ToLower(line)
	next := current
	switch {
	case strings.Contains(lower, "waiting for ssh") || strings.Contains(lower, "waiting for winrm"):
		next = 45
	case strings.Contains(lower, "provisioning with") || strings.Contains(lower, "provisioner"):
		next = 70
	case strings.Contains(lower, "template") || strings.Contains(lower, "shutting down"):
		next = 90
	case strings.Contains(lower, "creating") || strings.Contains(lower, "starting"):
		next = 15
	default:
		if next < 8 {
			next = 8
		}
	}
	if next < current {
		return current
	}
	return next
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func stripANSI(value string) string {
	return ansiPattern.ReplaceAllString(value, "")
}
