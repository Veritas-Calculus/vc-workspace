package pve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Config struct {
	Endpoint         string
	Username         string
	Password         string
	TokenID          string
	TokenSecret      string
	MutationsEnabled bool
	HTTPClient       *http.Client
}

type Client struct {
	endpoint         string
	username         string
	password         string
	tokenID          string
	tokenSecret      string
	mutationsEnabled bool
	httpClient       *http.Client

	mu       sync.Mutex
	ticket   string
	csrf     string
	ticketAt time.Time
}

type Summary struct {
	Version  Version   `json:"version"`
	Cluster  Cluster   `json:"cluster"`
	Nodes    []Node    `json:"nodes"`
	VMs      []VM      `json:"virtual_machines"`
	Storage  []Storage `json:"storage"`
	Writable bool      `json:"mutations_enabled"`
}

type Version struct {
	Version string `json:"version"`
	Release string `json:"release"`
}

type Cluster struct {
	Name      string `json:"name"`
	Quorate   bool   `json:"quorate"`
	NodeCount int    `json:"node_count"`
}

type Node struct {
	Name        string  `json:"name"`
	Status      string  `json:"status"`
	CPUUsage    float64 `json:"cpu_usage"`
	CPUCount    int     `json:"cpu_count"`
	MemoryUsed  int64   `json:"memory_used"`
	MemoryTotal int64   `json:"memory_total"`
}

type VM struct {
	VMID        int      `json:"vmid"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Node        string   `json:"node"`
	Status      string   `json:"status"`
	Template    bool     `json:"template"`
	Managed     bool     `json:"managed"`
	CPUCount    int      `json:"cpu_count"`
	MemoryTotal int64    `json:"memory_total"`
	Tags        []string `json:"-"`
}

// VMConfiguration is the normalized subset of a QEMU VM configuration that
// VC Workspace needs for image and passthrough validation. PVE represents several
// scalar values as either JSON strings or numbers depending on the field and
// version, so callers should use this type instead of decoding the API payload
// directly.
type VMConfiguration struct {
	Name            string            `json:"name"`
	OSType          string            `json:"os_type"`
	BIOS            string            `json:"bios"`
	Machine         string            `json:"machine"`
	SCSIController  string            `json:"scsi_controller"`
	AgentEnabled    bool              `json:"agent_enabled"`
	Cores           int               `json:"cores"`
	MemoryMB        int               `json:"memory_mb"`
	Tags            []string          `json:"tags"`
	EFIDisk         string            `json:"efi_disk,omitempty"`
	TPMState        string            `json:"tpm_state,omitempty"`
	Disks           map[string]string `json:"disks"`
	NetworkAdapters map[string]string `json:"network_adapters"`
	PCIHostDevices  map[string]string `json:"pci_host_devices"`
}

type Storage struct {
	Name    string `json:"name"`
	Kind    string `json:"kind"`
	Content string `json:"content"`
	Shared  bool   `json:"shared"`
}

type GuestNetworkInterface struct {
	Name            string           `json:"name"`
	HardwareAddress string           `json:"hardware_address,omitempty"`
	IPAddresses     []GuestIPAddress `json:"ip_addresses"`
}

type GuestIPAddress struct {
	Address string `json:"address"`
	Kind    string `json:"kind"`
	Prefix  int    `json:"prefix"`
}

type GuestExecResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

type GPUDevice struct {
	Node              string     `json:"node"`
	ID                string     `json:"id"`
	VendorID          string     `json:"vendor_id"`
	DeviceID          string     `json:"device_id"`
	SubsystemVendorID string     `json:"subsystem_vendor_id,omitempty"`
	SubsystemDeviceID string     `json:"subsystem_device_id,omitempty"`
	VendorName        string     `json:"vendor_name"`
	DeviceName        string     `json:"device_name"`
	Class             string     `json:"class"`
	IOMMUGroup        *int       `json:"iommu_group"`
	MDevCapable       bool       `json:"mdev_capable"`
	MDevTypes         []MDevType `json:"mdev_types"`
	Assignable        bool       `json:"assignable"`
}

type MDevType struct {
	Type        string `json:"type"`
	Name        string `json:"name"`
	Available   int    `json:"available"`
	Description string `json:"description"`
}

type PCIResourceMapping struct {
	ID          string                    `json:"id"`
	Description string                    `json:"description"`
	MDev        bool                      `json:"mdev"`
	Entries     []PCIResourceMappingEntry `json:"entries"`
}

type PCIResourceMappingEntry struct {
	Node        string   `json:"node"`
	DeviceID    string   `json:"device_id"`
	DevicePaths []string `json:"device_paths"`
	HardwareID  string   `json:"hardware_id"`
	SubsystemID string   `json:"subsystem_id,omitempty"`
	IOMMUGroup  string   `json:"iommu_group,omitempty"`
}

func New(cfg Config) (*Client, error) {
	endpoint := strings.TrimRight(cfg.Endpoint, "/")
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return nil, fmt.Errorf("PVE endpoint must be an absolute HTTPS URL")
	}
	passwordAuth := cfg.Username != "" && cfg.Password != ""
	tokenAuth := cfg.TokenID != "" && cfg.TokenSecret != ""
	if passwordAuth == tokenAuth {
		return nil, fmt.Errorf("configure exactly one PVE authentication method")
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		endpoint:         endpoint,
		username:         cfg.Username,
		password:         cfg.Password,
		tokenID:          cfg.TokenID,
		tokenSecret:      cfg.TokenSecret,
		mutationsEnabled: cfg.MutationsEnabled,
		httpClient:       httpClient,
	}, nil
}

func (c *Client) Summary(ctx context.Context) (Summary, error) {
	var versionResponse envelope[struct {
		Version string `json:"version"`
		Release string `json:"release"`
	}]
	if err := c.get(ctx, "/version", &versionResponse); err != nil {
		return Summary{}, err
	}

	var clusterResponse envelope[[]struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Quorate int    `json:"quorate"`
		Nodes   int    `json:"nodes"`
	}]
	if err := c.get(ctx, "/cluster/status", &clusterResponse); err != nil {
		return Summary{}, err
	}

	var nodesResponse envelope[[]struct {
		Node   string  `json:"node"`
		Status string  `json:"status"`
		CPU    float64 `json:"cpu"`
		MaxCPU int     `json:"maxcpu"`
		Mem    int64   `json:"mem"`
		MaxMem int64   `json:"maxmem"`
	}]
	if err := c.get(ctx, "/nodes", &nodesResponse); err != nil {
		return Summary{}, err
	}

	var vmResponse envelope[[]struct {
		VMID     int    `json:"vmid"`
		Name     string `json:"name"`
		Type     string `json:"type"`
		Node     string `json:"node"`
		Status   string `json:"status"`
		Template int    `json:"template"`
		Tags     string `json:"tags"`
		MaxCPU   int    `json:"maxcpu"`
		MaxMem   int64  `json:"maxmem"`
	}]
	if err := c.get(ctx, "/cluster/resources?type=vm", &vmResponse); err != nil {
		return Summary{}, err
	}

	var storageResponse envelope[[]struct {
		Storage string `json:"storage"`
		Type    string `json:"type"`
		Content string `json:"content"`
		Shared  int    `json:"shared"`
	}]
	if err := c.get(ctx, "/storage", &storageResponse); err != nil {
		return Summary{}, err
	}

	result := Summary{
		Version:  Version{Version: versionResponse.Data.Version, Release: versionResponse.Data.Release},
		Writable: c.mutationsEnabled,
	}
	for _, item := range clusterResponse.Data {
		if item.Type == "cluster" {
			result.Cluster = Cluster{Name: item.Name, Quorate: item.Quorate == 1, NodeCount: item.Nodes}
			break
		}
	}
	for _, item := range nodesResponse.Data {
		result.Nodes = append(result.Nodes, Node{
			Name: item.Node, Status: item.Status, CPUUsage: item.CPU,
			CPUCount: item.MaxCPU, MemoryUsed: item.Mem, MemoryTotal: item.MaxMem,
		})
	}
	for _, item := range vmResponse.Data {
		result.VMs = append(result.VMs, VM{
			VMID: item.VMID, Name: item.Name, Kind: item.Type, Node: item.Node,
			Status: item.Status, Template: item.Template == 1,
			CPUCount: item.MaxCPU, MemoryTotal: item.MaxMem, Tags: splitPVETags(item.Tags),
		})
	}
	for _, item := range storageResponse.Data {
		result.Storage = append(result.Storage, Storage{
			Name: item.Storage, Kind: item.Type, Content: item.Content, Shared: item.Shared == 1,
		})
	}
	sort.Slice(result.Nodes, func(i, j int) bool { return result.Nodes[i].Name < result.Nodes[j].Name })
	sort.Slice(result.VMs, func(i, j int) bool {
		if result.VMs[i].VMID == result.VMs[j].VMID {
			return result.VMs[i].Kind < result.VMs[j].Kind
		}
		return result.VMs[i].VMID < result.VMs[j].VMID
	})
	sort.Slice(result.Storage, func(i, j int) bool { return result.Storage[i].Name < result.Storage[j].Name })
	return result, nil
}

func (c *Client) NextVMID(ctx context.Context) (int, error) {
	var response envelope[string]
	if err := c.get(ctx, "/cluster/nextid", &response); err != nil {
		return 0, err
	}
	value, err := strconv.Atoi(response.Data)
	if err != nil {
		return 0, fmt.Errorf("decode next VMID: %w", err)
	}
	return value, nil
}

func (c *Client) VMConfiguration(ctx context.Context, node string, vmid int) (VMConfiguration, error) {
	if strings.TrimSpace(node) == "" || vmid <= 0 {
		return VMConfiguration{}, errors.New("VM configuration request is invalid")
	}
	var response envelope[map[string]json.RawMessage]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	if err := c.get(ctx, path, &response); err != nil {
		return VMConfiguration{}, err
	}
	value := func(key string) string { return rawScalarString(response.Data[key]) }
	configuration := VMConfiguration{
		Name:            value("name"),
		OSType:          value("ostype"),
		BIOS:            value("bios"),
		Machine:         value("machine"),
		SCSIController:  value("scsihw"),
		EFIDisk:         value("efidisk0"),
		TPMState:        value("tpmstate0"),
		Disks:           map[string]string{},
		NetworkAdapters: map[string]string{},
		PCIHostDevices:  map[string]string{},
	}
	configuration.Cores, _ = strconv.Atoi(value("cores"))
	configuration.MemoryMB, _ = strconv.Atoi(value("memory"))
	agent := value("agent")
	configuration.AgentEnabled = agent == "1" || strings.Contains(agent, "enabled=1")
	configuration.Tags = splitPVETags(value("tags"))
	diskPattern := regexp.MustCompile(`^(ide|sata|scsi|virtio)\d+$`)
	networkPattern := regexp.MustCompile(`^net\d+$`)
	pciPattern := regexp.MustCompile(`^hostpci\d+$`)
	for key := range response.Data {
		switch {
		case diskPattern.MatchString(key):
			configuration.Disks[key] = value(key)
		case networkPattern.MatchString(key):
			configuration.NetworkAdapters[key] = value(key)
		case pciPattern.MatchString(key):
			configuration.PCIHostDevices[key] = value(key)
		}
	}
	return configuration, nil
}

func rawScalarString(raw json.RawMessage) string {
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return strings.Trim(string(raw), `"`)
}

func splitPVETags(value string) []string {
	fields := strings.FieldsFunc(value, func(r rune) bool { return r == ';' || r == ',' })
	tags := make([]string, 0, len(fields))
	for _, field := range fields {
		if tag := strings.TrimSpace(field); tag != "" {
			tags = append(tags, tag)
		}
	}
	sort.Strings(tags)
	return tags
}

type CloneRequest struct {
	SourceNode string
	SourceVMID int
	TargetVMID int
	Name       string
	TargetNode string
	Storage    string
	Full       bool
}

type TaskStatus struct {
	Status     string `json:"status"`
	ExitStatus string `json:"exit_status,omitempty"`
}

func (c *Client) CloneTemplate(ctx context.Context, request CloneRequest) (string, error) {
	if request.SourceNode == "" || request.SourceVMID <= 0 || request.TargetVMID <= 0 || request.Name == "" {
		return "", errors.New("clone request is incomplete")
	}
	form := url.Values{
		"newid": {strconv.Itoa(request.TargetVMID)},
		"name":  {request.Name},
		"full":  {"0"},
	}
	if request.Full {
		form.Set("full", "1")
	}
	if request.TargetNode != "" {
		form.Set("target", request.TargetNode)
	}
	if request.Storage != "" && request.Full {
		form.Set("storage", request.Storage)
	}
	var response envelope[string]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/clone", url.PathEscape(request.SourceNode), request.SourceVMID)
	if err := c.do(ctx, http.MethodPost, path, form, &response); err != nil {
		return "", err
	}
	if response.Data == "" {
		return "", errors.New("PVE clone returned no task identifier")
	}
	return response.Data, nil
}

func (c *Client) TaskStatus(ctx context.Context, node, upid string) (TaskStatus, error) {
	var response envelope[struct {
		Status     string `json:"status"`
		ExitStatus string `json:"exitstatus"`
	}]
	path := fmt.Sprintf("/nodes/%s/tasks/%s/status", url.PathEscape(node), url.PathEscape(upid))
	if err := c.get(ctx, path, &response); err != nil {
		return TaskStatus{}, err
	}
	return TaskStatus{Status: response.Data.Status, ExitStatus: response.Data.ExitStatus}, nil
}

func (c *Client) ConfigureVMPCIResourceMapping(ctx context.Context, node string, vmid int, mapping string) error {
	mapping = strings.TrimSpace(mapping)
	if node == "" || vmid <= 0 || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(mapping) {
		return errors.New("PCI resource mapping request is invalid")
	}
	form := url.Values{
		"hostpci0": {"mapping=" + mapping + ",pcie=1,x-vga=1"},
	}
	var response envelope[any]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	return c.do(ctx, http.MethodPut, path, form, &response)
}

func (c *Client) ConfigureVMMDevResourceMapping(ctx context.Context, node string, vmid int, mapping, mdevType string) error {
	mapping = strings.TrimSpace(mapping)
	mdevType = strings.TrimSpace(mdevType)
	if node == "" || vmid <= 0 || !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(mapping) || !regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`).MatchString(mdevType) {
		return errors.New("mediated PCI resource mapping request is invalid")
	}
	form := url.Values{
		"hostpci0": {"mapping=" + mapping + ",mdev=" + mdevType},
	}
	var response envelope[any]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/config", url.PathEscape(node), vmid)
	return c.do(ctx, http.MethodPut, path, form, &response)
}

func (c *Client) CreatePCIResourceMapping(ctx context.Context, mapping PCIResourceMapping) error {
	form, err := pciResourceMappingForm(mapping)
	if err != nil {
		return err
	}
	form.Set("id", mapping.ID)
	var response envelope[any]
	return c.do(ctx, http.MethodPost, "/cluster/mapping/pci", form, &response)
}

func (c *Client) UpdatePCIResourceMapping(ctx context.Context, mapping PCIResourceMapping) error {
	form, err := pciResourceMappingForm(mapping)
	if err != nil {
		return err
	}
	var response envelope[any]
	path := "/cluster/mapping/pci/" + url.PathEscape(mapping.ID)
	return c.do(ctx, http.MethodPut, path, form, &response)
}

func pciResourceMappingForm(mapping PCIResourceMapping) (url.Values, error) {
	mapping.ID = strings.TrimSpace(mapping.ID)
	if !regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]{0,63}$`).MatchString(mapping.ID) || len(mapping.Entries) == 0 || len(mapping.Description) > 256 {
		return nil, errors.New("PCI resource mapping is invalid")
	}
	form := url.Values{}
	if mapping.MDev {
		form.Set("mdev", "1")
	}
	if description := strings.TrimSpace(mapping.Description); description != "" {
		form.Set("description", description)
	}
	for _, entry := range mapping.Entries {
		entry.Node = strings.TrimSpace(entry.Node)
		paths := entry.DevicePaths
		if len(paths) == 0 && entry.DeviceID != "" {
			paths = []string{entry.DeviceID}
		}
		normalizedPaths := make([]string, 0, len(paths))
		for _, path := range paths {
			normalized := normalizePCIPath(path)
			if normalized == "" {
				return nil, errors.New("PCI resource mapping contains an invalid device path")
			}
			normalizedPaths = append(normalizedPaths, normalized)
		}
		hardwareID := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(entry.HardwareID), "0x", ""))
		if entry.Node == "" || len(normalizedPaths) == 0 || !regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`).MatchString(hardwareID) {
			return nil, errors.New("PCI resource mapping entry is incomplete")
		}
		properties := []string{"node=" + entry.Node, "path=" + strings.Join(normalizedPaths, ";"), "id=" + hardwareID}
		if subsystemID := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(entry.SubsystemID), "0x", "")); subsystemID != "" {
			if !regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`).MatchString(subsystemID) {
				return nil, errors.New("PCI resource mapping contains an invalid subsystem ID")
			}
			properties = append(properties, "subsystem-id="+subsystemID)
		}
		if entry.IOMMUGroup != "" {
			group, err := strconv.Atoi(entry.IOMMUGroup)
			if err != nil || group < 0 {
				return nil, errors.New("PCI resource mapping contains an invalid IOMMU group")
			}
			properties = append(properties, "iommugroup="+strconv.Itoa(group))
		}
		form.Add("map", strings.Join(properties, ","))
	}
	return form, nil
}

func (c *Client) ChangePowerState(ctx context.Context, node string, vmid int, action string) (string, error) {
	if node == "" || vmid <= 0 || (action != "start" && action != "stop" && action != "shutdown") {
		return "", errors.New("power request is invalid")
	}
	var response envelope[string]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/%s", url.PathEscape(node), vmid, action)
	if err := c.do(ctx, http.MethodPost, path, url.Values{}, &response); err != nil {
		return "", err
	}
	if response.Data == "" {
		return "", errors.New("PVE power operation returned no task identifier")
	}
	return response.Data, nil
}

func (c *Client) GuestNetworkInterfaces(ctx context.Context, node string, vmid int) ([]GuestNetworkInterface, error) {
	if node == "" || vmid <= 0 {
		return nil, errors.New("guest network request is invalid")
	}
	var response envelope[struct {
		Result []struct {
			Name            string `json:"name"`
			HardwareAddress string `json:"hardware-address"`
			IPAddresses     []struct {
				Address string `json:"ip-address"`
				Kind    string `json:"ip-address-type"`
				Prefix  int    `json:"prefix"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", url.PathEscape(node), vmid)
	if err := c.get(ctx, path, &response); err != nil {
		return nil, err
	}
	interfaces := make([]GuestNetworkInterface, 0, len(response.Data.Result))
	for _, item := range response.Data.Result {
		guestInterface := GuestNetworkInterface{Name: item.Name, HardwareAddress: item.HardwareAddress}
		for _, address := range item.IPAddresses {
			guestInterface.IPAddresses = append(guestInterface.IPAddresses, GuestIPAddress{
				Address: address.Address,
				Kind:    address.Kind,
				Prefix:  address.Prefix,
			})
		}
		interfaces = append(interfaces, guestInterface)
	}
	return interfaces, nil
}

func (c *Client) ReadGuestFile(ctx context.Context, node string, vmid int, filename string, count int) (string, error) {
	if node == "" || vmid <= 0 || filename == "" || count <= 0 || count > 16*1024*1024 {
		return "", errors.New("guest file request is invalid")
	}
	var response envelope[struct {
		Content string `json:"content"`
	}]
	path := fmt.Sprintf(
		"/nodes/%s/qemu/%d/agent/file-read?file=%s&count=%d",
		url.PathEscape(node), vmid, url.QueryEscape(filename), count,
	)
	if err := c.get(ctx, path, &response); err != nil {
		return "", err
	}
	return response.Data.Content, nil
}

func (c *Client) SetGuestUserPassword(ctx context.Context, node string, vmid int, username, password string) error {
	if node == "" || vmid <= 0 || username == "" || len(password) < 5 {
		return errors.New("guest password request is invalid")
	}
	form := url.Values{
		"username": {username},
		"password": {password},
	}
	var response envelope[struct {
		Result any `json:"result"`
	}]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/set-user-password", url.PathEscape(node), vmid)
	if err := c.do(ctx, http.MethodPost, path, form, &response); err != nil {
		return err
	}
	return nil
}

// ExecGuest runs one fixed argv-style command through the QEMU Guest Agent and
// waits for its bounded result. Callers must never pass user-provided command
// fragments; this is the privileged enforcement channel used by control-plane
// policies.
func (c *Client) ExecGuest(ctx context.Context, node string, vmid int, command []string) (GuestExecResult, error) {
	if node == "" || vmid <= 0 || len(command) == 0 || len(command) > 64 {
		return GuestExecResult{}, errors.New("guest command request is invalid")
	}
	for _, argument := range command {
		if argument == "" || len(argument) > 128*1024 {
			return GuestExecResult{}, errors.New("guest command request is invalid")
		}
	}
	form := url.Values{"command": command}
	var started envelope[struct {
		PID int `json:"pid"`
	}]
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec", url.PathEscape(node), vmid)
	if err := c.do(ctx, http.MethodPost, path, form, &started); err != nil {
		return GuestExecResult{}, err
	}
	if started.Data.PID <= 0 {
		return GuestExecResult{}, errors.New("guest agent returned no process identifier")
	}

	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		statusPath := fmt.Sprintf("/nodes/%s/qemu/%d/agent/exec-status?pid=%d", url.PathEscape(node), vmid, started.Data.PID)
		var status envelope[struct {
			Exited   int    `json:"exited"`
			ExitCode int    `json:"exitcode"`
			Stdout   string `json:"out-data"`
			Stderr   string `json:"err-data"`
		}]
		if err := c.get(ctx, statusPath, &status); err != nil {
			if !isTransientGuestExecStatusError(err) {
				return GuestExecResult{}, err
			}
			select {
			case <-ctx.Done():
				return GuestExecResult{}, ctx.Err()
			case <-ticker.C:
				continue
			}
		}
		if status.Data.Exited == 1 {
			return GuestExecResult{ExitCode: status.Data.ExitCode, Stdout: status.Data.Stdout, Stderr: status.Data.Stderr}, nil
		}
		select {
		case <-ctx.Done():
			return GuestExecResult{}, ctx.Err()
		case <-ticker.C:
		}
	}
}

func isTransientGuestExecStatusError(err error) bool {
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "guest-exec-status") && strings.Contains(message, "timeout")
}

func (c *Client) GPUDevices(ctx context.Context, nodes []string) ([]GPUDevice, error) {
	devices := make([]GPUDevice, 0)
	for _, node := range nodes {
		if strings.TrimSpace(node) == "" {
			return nil, errors.New("GPU inventory request contains an empty node")
		}
		var response envelope[[]struct {
			ID                string          `json:"id"`
			VendorID          string          `json:"vendor"`
			DeviceID          string          `json:"device"`
			SubsystemVendorID string          `json:"subsystem_vendor"`
			SubsystemDeviceID string          `json:"subsystem_device"`
			VendorName        string          `json:"vendor_name"`
			DeviceName        string          `json:"device_name"`
			Class             string          `json:"class"`
			IOMMUGroup        *int            `json:"iommu_group"`
			IOMMUGroupPVE     *int            `json:"iommugroup"`
			MDev              json.RawMessage `json:"mdev"`
		}]
		path := fmt.Sprintf("/nodes/%s/hardware/pci", url.PathEscape(node))
		if err := c.get(ctx, path, &response); err != nil {
			return nil, err
		}
		for _, item := range response.Data {
			if !strings.HasPrefix(strings.ToLower(item.Class), "0x03") {
				continue
			}
			mdev := strings.TrimSpace(string(item.MDev))
			mdevCapable := mdev != "" && mdev != "0" && mdev != "false" && mdev != "null" && mdev != "{}" && mdev != "[]"
			mdevTypes := inlineMDevTypes(item.MDev)
			if mdevCapable && len(mdevTypes) == 0 {
				var mdevResponse envelope[[]MDevType]
				mdevPath := fmt.Sprintf("/nodes/%s/hardware/pci/%s/mdev", url.PathEscape(node), url.PathEscape(item.ID))
				if err := c.get(ctx, mdevPath, &mdevResponse); err != nil {
					return nil, err
				}
				mdevTypes = mdevResponse.Data
			}
			sort.Slice(mdevTypes, func(i, j int) bool { return mdevTypes[i].Type < mdevTypes[j].Type })
			iommuGroup := item.IOMMUGroup
			if iommuGroup == nil {
				iommuGroup = item.IOMMUGroupPVE
			}
			assignable := iommuGroup != nil && *iommuGroup > 0
			devices = append(devices, GPUDevice{
				Node: node, ID: item.ID, VendorID: strings.ToLower(item.VendorID), DeviceID: strings.ToLower(item.DeviceID),
				SubsystemVendorID: strings.ToLower(item.SubsystemVendorID), SubsystemDeviceID: strings.ToLower(item.SubsystemDeviceID),
				VendorName: item.VendorName, DeviceName: item.DeviceName, Class: strings.ToLower(item.Class),
				IOMMUGroup: iommuGroup, MDevCapable: mdevCapable, MDevTypes: mdevTypes, Assignable: assignable,
			})
		}
	}
	sort.Slice(devices, func(i, j int) bool {
		if devices[i].Node == devices[j].Node {
			return devices[i].ID < devices[j].ID
		}
		return devices[i].Node < devices[j].Node
	})
	return devices, nil
}

func inlineMDevTypes(raw json.RawMessage) []MDevType {
	var values map[string]struct {
		Available   int    `json:"available"`
		Name        string `json:"name"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil
	}
	result := make([]MDevType, 0, len(values))
	for kind, value := range values {
		if strings.TrimSpace(kind) == "" {
			continue
		}
		result = append(result, MDevType{Type: kind, Name: value.Name, Available: value.Available, Description: value.Description})
	}
	return result
}

func (c *Client) PCIResourceMappings(ctx context.Context) ([]PCIResourceMapping, error) {
	var response envelope[[]struct {
		ID          string          `json:"id"`
		Description string          `json:"description"`
		MDev        json.RawMessage `json:"mdev"`
		Map         []string        `json:"map"`
	}]
	if err := c.get(ctx, "/cluster/mapping/pci", &response); err != nil {
		return nil, err
	}
	mappings := make([]PCIResourceMapping, 0, len(response.Data))
	for _, item := range response.Data {
		mdev := strings.TrimSpace(string(item.MDev))
		mapping := PCIResourceMapping{ID: item.ID, Description: item.Description, MDev: mdev == "1" || mdev == "true" || mdev == `"1"`, Entries: make([]PCIResourceMappingEntry, 0, len(item.Map))}
		for _, raw := range item.Map {
			properties := parsePropertyString(raw)
			paths := make([]string, 0)
			for _, path := range strings.Split(properties["path"], ";") {
				if normalized := normalizePCIPath(path); normalized != "" {
					paths = append(paths, normalized)
				}
			}
			// Older fixtures and early PVE mapping prototypes used id for the
			// address. Keep that form readable, but current PVE always uses path
			// for the BDF and id for vendor:device identity.
			if len(paths) == 0 {
				if normalized := normalizePCIPath(properties["id"]); normalized != "" {
					paths = append(paths, normalized)
				}
			}
			entry := PCIResourceMappingEntry{
				Node: properties["node"], DevicePaths: paths, HardwareID: strings.ToLower(properties["id"]),
				SubsystemID: strings.ToLower(properties["subsystem-id"]), IOMMUGroup: properties["iommugroup"],
			}
			if len(paths) > 0 {
				entry.DeviceID = paths[0]
			}
			if entry.Node == "" || entry.DeviceID == "" {
				return nil, fmt.Errorf("PCI resource mapping %q contains an incomplete entry", item.ID)
			}
			mapping.Entries = append(mapping.Entries, entry)
		}
		mappings = append(mappings, mapping)
	}
	sort.Slice(mappings, func(i, j int) bool { return mappings[i].ID < mappings[j].ID })
	return mappings, nil
}

func parsePropertyString(value string) map[string]string {
	result := make(map[string]string)
	for _, field := range strings.Split(value, ",") {
		key, item, found := strings.Cut(field, "=")
		if found {
			result[strings.TrimSpace(key)] = strings.TrimSpace(item)
		}
	}
	return result
}

func normalizePCIPath(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if regexp.MustCompile(`^[0-9a-f]{2}:[0-9a-f]{2}(?:\.[0-7])?$`).MatchString(value) {
		return "0000:" + value
	}
	if !regexp.MustCompile(`^[0-9a-f]{4,}:[0-9a-f]{2}:[0-9a-f]{2}(?:\.[0-7])?$`).MatchString(value) {
		return ""
	}
	return value
}

func (c *Client) get(ctx context.Context, path string, target any) error {
	return c.do(ctx, http.MethodGet, path, nil, target)
}

func (c *Client) do(ctx context.Context, method, path string, form url.Values, target any) error {
	if method != http.MethodGet && method != http.MethodHead && !c.mutationsEnabled {
		return errors.New("PVE mutations are disabled")
	}
	if c.tokenID == "" {
		if err := c.ensureTicket(ctx); err != nil {
			return err
		}
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.endpoint+"/api2/json"+path, body)
	if err != nil {
		return fmt.Errorf("build PVE request: %w", err)
	}
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if c.tokenID != "" {
		req.Header.Set("Authorization", "PVEAPIToken="+c.tokenID+"="+c.tokenSecret)
	} else {
		c.mu.Lock()
		req.AddCookie(&http.Cookie{Name: "PVEAuthCookie", Value: c.ticket})
		if method != http.MethodGet && method != http.MethodHead {
			req.Header.Set("CSRFPreventionToken", c.csrf)
		}
		c.mu.Unlock()
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("call PVE: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("PVE returned %s: %s", resp.Status, strings.TrimSpace(string(message)))
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return fmt.Errorf("decode PVE response: %w", err)
	}
	return nil
}

func (c *Client) ensureTicket(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ticket != "" && time.Since(c.ticketAt) < 90*time.Minute {
		return nil
	}
	form := url.Values{"username": {c.username}, "password": {c.password}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/api2/json/access/ticket", strings.NewReader(form.Encode()))
	if err != nil {
		return fmt.Errorf("build PVE authentication request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("authenticate with PVE: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("PVE authentication returned %s", resp.Status)
	}
	var auth envelope[struct {
		Ticket string `json:"ticket"`
		CSRF   string `json:"CSRFPreventionToken"`
	}]
	if err := json.NewDecoder(resp.Body).Decode(&auth); err != nil {
		return fmt.Errorf("decode PVE authentication: %w", err)
	}
	if auth.Data.Ticket == "" {
		return errors.New("PVE authentication returned no ticket")
	}
	c.ticket = auth.Data.Ticket
	c.csrf = auth.Data.CSRF
	c.ticketAt = time.Now()
	return nil
}

type envelope[T any] struct {
	Data T `json:"data"`
}
