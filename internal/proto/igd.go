package proto

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/netutil"
)

// UPnP / IGD service types.
const (
	svcWANIPConnection1 = "urn:schemas-upnp-org:service:WANIPConnection:1"
	svcWANIPConnection2 = "urn:schemas-upnp-org:service:WANIPConnection:2"
	svcWANPPPConn1      = "urn:schemas-upnp-org:service:WANPPPConnection:1"
	svcLayer3Forwarding = "urn:schemas-upnp-org:service:Layer3Forwarding:1"
	svcWANCommonIFC1    = "urn:schemas-upnp-org:service:WANCommonInterfaceConfig:1"
)

// IGDClient speaks SOAP to a UPnP Internet Gateway Device. It supports both
// IGD v2 (WANIPConnection:2, AddAnyPortMapping) and the legacy v1 service.
type IGDClient struct {
	info        GatewayInfo
	controlURL  string
	serviceType string
	http        *http.Client
	version     int // 2 for IGD:2, 1 for legacy
	legacyPPP   bool
	cachedExt   string
	// v6 holds the optional WANIPv6FirewallControl:1 endpoint and its cached
	// firewall status. nil when the device description did not advertise it.
	v6 *ipv6State
}

// IGDConfig describes where to reach the device.
type IGDConfig struct {
	Kind         string // igd2|igd1
	Address      string
	Port         int
	Location     string
	ControlURL   string
	ServiceType  string
	ModelName    string
	Manufacturer string
	UDN          string
	Name         string
	Timeout      time.Duration

	// IPv6FirewallControlURL/ServiceType describe the optional IGDv2
	// WANIPv6FirewallControl:1 service found in the device description. Both are
	// empty on gateways that cannot open IPv6 pinholes.
	IPv6FirewallControlURL  string
	IPv6FirewallServiceType string
}

// NewIGDClient creates a client for a discovered IGD.
func NewIGDClient(c IGDConfig) *IGDClient {
	timeout := c.Timeout
	if timeout <= 0 {
		timeout = 6 * time.Second
	}
	version := 2
	legacy := false
	if c.Kind == KindIGD1 {
		version = 1
	}
	if strings.Contains(c.ServiceType, "WANPPPConnection") {
		legacy = true
	}
	name := c.Name
	if name == "" {
		if c.ModelName != "" {
			name = c.ModelName
		} else {
			name = "IGD"
		}
	}
	caps := []string{"map-tcp", "map-udp", "external-ip", "delete", "enumerate", "get-status"}
	if version == 2 {
		caps = append(caps, "add-any-port", "igd-v2")
	} else {
		caps = append(caps, "igd-v1")
	}
	if legacy {
		caps = append(caps, "wan-ppp")
	}
	var v6 *ipv6State
	if c.IPv6FirewallControlURL != "" {
		// The service is optional and independent of the WAN connection version:
		// advertise it so callers can route IPv6 rules to AddPinhole.
		caps = append(caps, CapIPv6Pinhole)
		svcType := c.IPv6FirewallServiceType
		if svcType == "" {
			svcType = svcWANIPv6FirewallControl1
		}
		v6 = &ipv6State{controlURL: c.IPv6FirewallControlURL, serviceType: svcType}
	}
	return &IGDClient{
		info: GatewayInfo{
			Kind:         c.Kind,
			Name:         name,
			Address:      c.Address,
			Port:         c.Port,
			Location:     c.Location,
			ControlURL:   c.ControlURL,
			ServiceType:  c.ServiceType,
			ModelName:    c.ModelName,
			Manufacturer: c.Manufacturer,
			UDN:          c.UDN,
			Capabilities: caps,
		},
		controlURL:  c.ControlURL,
		serviceType: c.ServiceType,
		http:        &http.Client{Timeout: timeout},
		version:     version,
		legacyPPP:   legacy,
		v6:          v6,
	}
}

// Info returns gateway details.
func (c *IGDClient) Info() GatewayInfo { return c.info }

// Close releases the HTTP client.
func (c *IGDClient) Close() error {
	c.http.CloseIdleConnections()
	return nil
}

// soapAction performs a SOAP call on the WAN connection service.
func (c *IGDClient) soapAction(ctx context.Context, action string, args [][2]string) (string, error) {
	return c.soapActionAt(ctx, c.controlURL, c.serviceType, action, args)
}

// soapActionAt performs a SOAP call against an explicit control URL and service
// type, so the IPv6 firewall service can share the same envelope/fault handling.
func (c *IGDClient) soapActionAt(ctx context.Context, controlURL, serviceType, action string, args [][2]string) (string, error) {
	var body bytes.Buffer
	body.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	body.WriteString(`<s:Envelope xmlns:s="http://schemas.xmlsoap.org/soap/envelope/" s:encodingStyle="http://schemas.xmlsoap.org/soap/encoding/">`)
	body.WriteString(`<s:Body>`)
	fmt.Fprintf(&body, `<u:%s xmlns:u="%s">`, action, serviceType)
	for _, kv := range args {
		fmt.Fprintf(&body, `<%s>%s</%s>`, kv[0], xmlEscape(kv[1]), kv[0])
	}
	fmt.Fprintf(&body, `</u:%s>`, action)
	body.WriteString(`</s:Body></s:Envelope>`)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL, bytes.NewReader(body.Bytes()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", `text/xml; charset="utf-8"`)
	req.Header.Set("SOAPAction", fmt.Sprintf(`"%s#%s"`, serviceType, action))
	req.Header.Set("User-Agent", "NatPilot/0.1 UPnP/1.1")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("soap %s: %w", action, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("soap %s read: %w", action, err)
	}
	text := string(b)
	if resp.StatusCode >= 400 {
		if code, desc := parseUPnPError(text); code != "" {
			return "", upnpError(action, code, desc)
		}
		return "", fmt.Errorf("soap %s: http %d", action, resp.StatusCode)
	}
	if code, desc := parseUPnPError(text); code != "" {
		return "", upnpError(action, code, desc)
	}
	return text, nil
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}

// UPnPError is a SOAP fault returned by a gateway, with the raw error code kept
// so callers can react to specific conditions (e.g. 704 = pinhole already gone).
type UPnPError struct {
	Action      string
	Code        string
	Description string
}

// upnpHints carries the operator-facing explanation appended to the raw fault.
var upnpHints = map[string]string{
	// ---- WANIPConnection ----
	"606": "；路由器拒绝了该动作，常见原因：" +
		"① 网关启用了 secure mode / UPnP 访问控制，只允许把端口映射到『发起请求的本机 IP』；" +
		"② 该内网客户端地址不在网关的授权列表内；" +
		"③ 请求的端口超出网关允许的范围（很多固件禁用 1024 以下端口）；" +
		"④ 该网关只接受注册过的客户端。请在路由器 UPnP 设置里放开对本机的授权，" +
		"并把规则目标改为本机 IP、外部端口换成 1024 以上后重试",
	"718": "；该外部端口已被其它映射占用，换一个外部端口或先删除冲突项",
	"724": "；该网关要求内外网端口一致，请把外部端口留空或设为与内网端口相同",
	"725": "；该网关只支持永久租约，请把租约设为 0（永久）",
	"501": "；网关执行失败，可能是端口被占用或网关临时错误",

	// ---- WANIPv6FirewallControl:1 (pinhole) ----
	"701": "；网关的 IPv6 pinhole 表已满（PinholeSpaceExhausted），请先删除不再需要的 IPv6 规则/防火墙 pinhole 后重试",
	"702": "；网关的 IPv6 防火墙已关闭（FirewallDisabled），请在路由器管理界面启用 IPv6 防火墙后重试",
	"703": "；网关不允许入站 IPv6 pinhole（InboundPinholeNotAllowed），请在路由器 IPv6 防火墙设置中允许入站连接，或手动添加 pinhole 规则",
	"704": "；该 pinhole 不存在（NoSuchEntry），可能已过期或被网关清除，可忽略或重新创建",
	"705": "；网关不支持该协议（ProtocolNotSupported），IPv6 pinhole 仅支持 TCP(6) 或 UDP(17)",
	"706": "；网关不允许内网端口通配（InternalPortWildcardingNotAllowed），请为 pinhole 指定具体的内网端口",
	"707": "；网关不允许协议通配（ProtocolWildcardingNotAllowed），请明确指定 TCP 或 UDP",
	"708": "；网关不允许完全通配来源地址（WildcardNotPermittedInSrcIP），请指定来源 IPv6 地址/端口后重试",
	"709": "；网关没有收到内网侧的探测包（NoPacketSent），请确认目标 IPv6 主机在线、本地防火墙放行，并检查该地址是否属于本网关的 LAN",
}

// Error renders the fault. The raw code stays first so logs remain greppable.
func (e *UPnPError) Error() string {
	msg := fmt.Sprintf("upnp error %s: %s", e.Code, e.Description)
	if hint, ok := upnpHints[e.Code]; ok {
		msg += hint
	}
	return msg
}

// upnpError turns a SOAP fault into an error with an actionable hint for the codes
// that operators actually hit.
func upnpError(action, code, desc string) error {
	return &UPnPError{Action: action, Code: code, Description: desc}
}

// UPnPErrorCode extracts the numeric UPnP error code from an error returned by
// this package. It reports false for non-UPnP errors.
func UPnPErrorCode(err error) (string, bool) {
	var ue *UPnPError
	if errors.As(err, &ue) {
		return ue.Code, true
	}
	return "", false
}

func parseUPnPError(body string) (string, string) {
	var env struct {
		Body struct {
			Fault struct {
				Detail struct {
					UPnPError struct {
						Code        string `xml:"errorCode"`
						Description string `xml:"errorDescription"`
					} `xml:"UPnPError"`
				} `xml:"detail"`
			} `xml:"Fault"`
		} `xml:"Body"`
	}
	if err := xml.Unmarshal([]byte(body), &env); err != nil {
		return "", ""
	}
	if env.Body.Fault.Detail.UPnPError.Code != "" {
		return env.Body.Fault.Detail.UPnPError.Code, env.Body.Fault.Detail.UPnPError.Description
	}
	return "", ""
}

// field extracts the text of the first element named name from a SOAP body.
func field(body, name string) string {
	open := "<" + name + ">"
	closeTag := "</" + name + ">"
	i := strings.Index(body, open)
	if i < 0 {
		// namespaced variant <u:name>
		open = ":" + name + ">"
		i = strings.Index(body, open)
		if i < 0 {
			return ""
		}
		i += len(open)
		j := strings.Index(body[i:], closeTag)
		if j < 0 {
			j = strings.Index(body[i:], ":"+name+">")
			if j < 0 {
				return ""
			}
		}
		return strings.TrimSpace(body[i : i+j])
	}
	i += len(open)
	j := strings.Index(body[i:], closeTag)
	if j < 0 {
		return ""
	}
	return strings.TrimSpace(body[i : i+j])
}

// ExternalIP queries GetExternalIPAddress.
func (c *IGDClient) ExternalIP(ctx context.Context) (string, error) {
	body, err := c.soapAction(ctx, "GetExternalIPAddress", nil)
	if err != nil {
		return "", err
	}
	ip := field(body, "NewExternalIPAddress")
	c.cachedExt = ip
	c.info.ExternalIP = ip
	return ip, nil
}

// StatusInfo returns connection status and uptime.
func (c *IGDClient) StatusInfo(ctx context.Context) (string, string, error) {
	body, err := c.soapAction(ctx, "GetStatusInfo", nil)
	if err != nil {
		return "", "", err
	}
	return field(body, "NewConnectionStatus"), field(body, "NewUptime"), nil
}

// AddMapping creates a port mapping. IGD v2 uses AddAnyPortMapping when the
// caller allows the gateway to pick a free external port.
func (c *IGDClient) AddMapping(ctx context.Context, m Mapping) (Result, error) {
	proto := strings.ToUpper(NormalizeProto(m.Protocol))
	if proto != "TCP" && proto != "UDP" {
		return Result{}, fmt.Errorf("igd: unsupported protocol %q", m.Protocol)
	}
	lease := m.LeaseSeconds
	if lease <= 0 {
		lease = 0 // 0 = permanent on IGD v1, infinite on v2
	}
	internalClient := m.InternalHost
	if internalClient == "" {
		internalClient = "0.0.0.0" // "this host" sentinel understood by IGD
	}
	extPort := m.ExternalPort
	if extPort == 0 {
		extPort = m.InternalPort
	}
	args := [][2]string{
		{"NewRemoteHost", m.RemoteHost},
		{"NewExternalPort", strconv.Itoa(extPort)},
		{"NewProtocol", proto},
		{"NewInternalPort", strconv.Itoa(m.InternalPort)},
		{"NewInternalClient", internalClient},
		{"NewEnabled", "1"},
		{"NewPortMappingDescription", truncate(m.Description, 64)},
		{"NewLeaseDuration", strconv.Itoa(lease)},
	}

	action := "AddPortMapping"
	useAnyPort := c.version == 2 && m.ExternalPort == 0
	if useAnyPort {
		action = "AddAnyPortMapping"
	}
	body, err := c.soapAction(ctx, action, args)
	if err != nil && useAnyPort {
		// Gateways advertising v2 sometimes only implement v1 verbs.
		body, err = c.soapAction(ctx, "AddPortMapping", args)
		useAnyPort = false
	}
	if err != nil {
		return Result{}, err
	}

	assigned := extPort
	if useAnyPort {
		if v := field(body, "NewReservedPort"); v != "" {
			if p, perr := strconv.Atoi(v); perr == nil {
				assigned = p
			}
		}
	}
	extIP := c.cachedExt
	if extIP == "" {
		extIP, _ = c.ExternalIP(ctx)
	}
	return Result{
		ExternalIP:   extIP,
		ExternalPort: assigned,
		InternalHost: internalClient,
		InternalPort: m.InternalPort,
		Protocol:     strings.ToLower(proto),
		LeaseSeconds: lease,
		GatewayKind:  c.info.Kind,
		GatewayName:  c.info.Name,
		AppliedAt:    time.Now(),
		Detail:       action,
	}, nil
}

// DeleteMapping removes a mapping via DeletePortMapping.
func (c *IGDClient) DeleteMapping(ctx context.Context, m Mapping) error {
	proto := strings.ToUpper(NormalizeProto(m.Protocol))
	extPort := m.ExternalPort
	if extPort == 0 {
		extPort = m.InternalPort
	}
	_, err := c.soapAction(ctx, "DeletePortMapping", [][2]string{
		{"NewRemoteHost", m.RemoteHost},
		{"NewExternalPort", strconv.Itoa(extPort)},
		{"NewProtocol", proto},
	})
	return err
}

// ListMappings enumerates mappings through GetGenericPortMappingEntry.
func (c *IGDClient) ListMappings(ctx context.Context) ([]Mapping, error) {
	out := []Mapping{}
	for i := 0; i < 512; i++ {
		body, err := c.soapAction(ctx, "GetGenericPortMappingEntry", [][2]string{
			{"NewPortMappingIndex", strconv.Itoa(i)},
		})
		if err != nil {
			if i == 0 {
				return nil, ErrNotSupported
			}
			break
		}
		m := Mapping{
			RemoteHost:   field(body, "NewRemoteHost"),
			Protocol:     strings.ToLower(field(body, "NewProtocol")),
			InternalPort: atoiSafe(field(body, "NewInternalPort")),
			InternalHost: field(body, "NewInternalClient"),
			ExternalPort: atoiSafe(field(body, "NewExternalPort")),
			Description:  field(body, "NewPortMappingDescription"),
			LeaseSeconds: atoiSafe(field(body, "NewLeaseDuration")),
			Enabled:      field(body, "NewEnabled") == "1",
		}
		if m.ExternalPort == 0 {
			break
		}
		out = append(out, m)
	}
	return out, nil
}

func atoiSafe(s string) int {
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return n
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// ---- SSDP discovery ----

// SSDPDevice is a discovery response.
type SSDPDevice struct {
	Location string
	ST       string
	USN      string
	Server   string
	Address  string
	Port     int
}

// SSDPDiscover performs an M-SEARCH on every usable IPv4 interface.
//
// One socket per interface is opened and IP_MULTICAST_IF is pinned to that
// interface, because a desktop with a VPN/TUN/Hyper-V adapter otherwise sends
// the M-SEARCH out of the tunnel: the kernel picks the egress interface from the
// multicast route, which follows the default route, and the LAN router never
// sees the request. Replies are unicast back to the source address, so no IGMP
// membership is needed.
//
// sources may be nil, in which case every up, non-loopback IPv4 address is used.
func SSDPDiscover(ctx context.Context, targets []string, sources []net.IP, timeout time.Duration) ([]SSDPDevice, error) {
	if timeout <= 0 {
		timeout = 4 * time.Second
	}
	if len(targets) == 0 {
		targets = defaultSearchTargets()
	}
	group := &net.UDPAddr{IP: net.ParseIP("239.255.255.250"), Port: 1900}

	ch := make(chan SSDPDevice, 256)
	conns := []*net.UDPConn{}
	for _, src := range ssdpSources(sources) {
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: src, Port: 0})
		if err != nil {
			logging.Debug("gateway", "ssdp socket failed", "interface", src.String(), "error", err.Error())
			continue
		}
		if err := netutil.SetMulticastInterface(conn, src); err != nil {
			logging.Debug("gateway", "ssdp multicast interface failed", "interface", src.String(), "error", err.Error())
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
		conns = append(conns, conn)
		go readSSDPReplies(conn, ch)
	}
	if len(conns) == 0 {
		// Last resort: a wildcard socket still works on single-homed hosts.
		conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
		if err != nil {
			return nil, fmt.Errorf("ssdp listen: %w", err)
		}
		_ = conn.SetReadDeadline(time.Now().Add(timeout))
		_ = conn.SetWriteDeadline(time.Now().Add(timeout))
		conns = append(conns, conn)
		go readSSDPReplies(conn, ch)
	}
	defer func() {
		for _, c := range conns {
			_ = c.Close()
		}
	}()

	for _, conn := range conns {
		for _, st := range targets {
			if _, err := conn.WriteToUDP([]byte(buildMSearch(st)), group); err != nil {
				logging.Debug("gateway", "ssdp send failed", "error", err.Error())
				break
			}
		}
	}

	deadline := time.Now().Add(timeout)
	seen := map[string]bool{}
	results := []SSDPDevice{}
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		wait := remaining
		if wait > 200*time.Millisecond {
			wait = 200 * time.Millisecond
		}
		select {
		case <-ctx.Done():
			return results, ctx.Err()
		case d := <-ch:
			key := d.Location + "|" + d.ST
			if d.USN != "" {
				key = d.USN + "|" + d.ST
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			results = append(results, d)
		case <-time.After(wait):
		}
	}
	return results, nil
}

// defaultSearchTargets lists the SSDP search targets for an IGD.
func defaultSearchTargets() []string {
	return []string{
		"urn:schemas-upnp-org:device:InternetGatewayDevice:2",
		"urn:schemas-upnp-org:device:InternetGatewayDevice:1",
		svcWANIPConnection2,
		svcWANIPConnection1,
		svcWANPPPConn1,
		"upnp:rootdevice",
	}
}

// ssdpSources resolves the addresses to send M-SEARCH from.
func ssdpSources(given []net.IP) []net.IP {
	if len(given) > 0 {
		return given
	}
	list, err := netutil.Interfaces()
	if err != nil {
		return nil
	}
	want := make([]net.IP, 0, len(list))
	for _, i := range list {
		if i.Loopback || !i.Up {
			continue
		}
		ip := net.ParseIP(i.IP)
		if ip == nil || ip.To4() == nil {
			continue
		}
		// Link-local adapters (169.254/16) are noise: nothing routable lives there.
		if ip.IsLinkLocalUnicast() {
			continue
		}
		want = append(want, ip)
	}
	return want
}

// readSSDPReplies pumps replies from one socket until it is closed.
func readSSDPReplies(conn *net.UDPConn, ch chan<- SSDPDevice) {
	buf := make([]byte, 8192)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		d := parseSSDPResponse(string(buf[:n]), addr)
		if d.Location == "" {
			continue
		}
		select {
		case ch <- d:
		default:
		}
	}
}

func buildMSearch(st string) string {
	return "M-SEARCH * HTTP/1.1\r\n" +
		"HOST: 239.255.255.250:1900\r\n" +
		"MAN: \"ssdp:discover\"\r\n" +
		"MX: 2\r\n" +
		"ST: " + st + "\r\n" +
		"USER-AGENT: NatPilot/0.1 UPnP/1.1\r\n" +
		"\r\n"
}

func parseSSDPResponse(raw string, addr *net.UDPAddr) SSDPDevice {
	d := SSDPDevice{Address: addr.IP.String(), Port: addr.Port}
	for _, line := range strings.Split(raw, "\r\n") {
		idx := strings.Index(line, ":")
		if idx <= 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(line[:idx]))
		val := strings.TrimSpace(line[idx+1:])
		switch key {
		case "LOCATION":
			d.Location = val
		case "ST":
			d.ST = val
		case "USN":
			d.USN = val
		case "SERVER":
			d.Server = val
		}
	}
	return d
}

// ---- device description ----

// deviceNode mirrors one <device> element (also used for nested devices).
type deviceNode struct {
	DeviceType   string `xml:"deviceType"`
	FriendlyName string `xml:"friendlyName"`
	Manufacturer string `xml:"manufacturer"`
	ModelName    string `xml:"modelName"`
	ModelNumber  string `xml:"modelNumber"`
	UDN          string `xml:"UDN"`
	ServiceList  struct {
		Services []struct {
			ServiceType string `xml:"serviceType"`
			ServiceID   string `xml:"serviceId"`
			ControlURL  string `xml:"controlURL"`
			EventSubURL string `xml:"eventSubURL"`
			SCPDURL     string `xml:"SCPDURL"`
		} `xml:"service"`
	} `xml:"serviceList"`
	DeviceList struct {
		Devices []deviceNode `xml:"device"`
	} `xml:"deviceList"`
	PresentationURL string `xml:"presentationURL"`
}

// deviceDescription is the root document of a UPnP device description.
type deviceDescription struct {
	SpecVersion struct {
		Major int `xml:"major"`
		Minor int `xml:"minor"`
	} `xml:"specVersion"`
	Device deviceNode `xml:"device"`
}

// flattenedServices collects every service of the device tree.
func (d *deviceDescription) flattenedServices() []flattenedService {
	return d.Device.flattenedServices()
}

type flattenedService struct {
	ServiceType string
	ControlURL  string
}

// flattenedServices walks a device node and its children.
func (n *deviceNode) flattenedServices() []flattenedService {
	out := []flattenedService{}
	for _, s := range n.ServiceList.Services {
		out = append(out, flattenedService{ServiceType: s.ServiceType, ControlURL: s.ControlURL})
	}
	for i := range n.DeviceList.Devices {
		out = append(out, n.DeviceList.Devices[i].flattenedServices()...)
	}
	return out
}

// DescribeGateway fetches a device description and selects the best WAN
// connection service (IGD v2 preferred, then v1, then PPP).
func DescribeGateway(ctx context.Context, location string, timeout time.Duration) (*IGDConfig, *deviceDescription, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, location, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "NatPilot/0.1 UPnP/1.1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("fetch description: %w", err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, nil, err
	}
	var desc deviceDescription
	if err := xml.Unmarshal(b, &desc); err != nil {
		return nil, nil, fmt.Errorf("parse description: %w", err)
	}
	base, _ := url.Parse(location)
	svcs := desc.flattenedServices()

	type candidate struct {
		kind    string
		rank    int
		svcType string
		ctrl    string
	}
	best := candidate{rank: 99}
	// The IPv6 firewall service lives next to the WAN connection service in the
	// same description (usually on the WANDevice node). Capture it even though
	// SSDP itself is IPv4-only: the pinhole service is what IPv6 needs.
	v6Ctrl := ""
	v6Svc := ""
	for _, s := range svcs {
		st := s.ServiceType
		if v6Ctrl == "" && strings.HasPrefix(st, svcWANIPv6FirewallControlPrefix) {
			v6Ctrl = resolveURL(base, s.ControlURL)
			v6Svc = st
		}
		c := candidate{svcType: st, ctrl: resolveURL(base, s.ControlURL)}
		switch {
		case strings.HasPrefix(st, svcWANIPConnection2):
			c.kind, c.rank = KindIGD2, 0
		case strings.HasPrefix(st, svcWANIPConnection1):
			c.kind, c.rank = KindIGD1, 1
		case strings.HasPrefix(st, svcWANPPPConn1):
			c.kind, c.rank = KindIGD1, 2
		default:
			continue
		}
		if c.rank < best.rank {
			best = c
		}
	}
	if best.rank == 99 {
		return nil, &desc, ErrNotSupported
	}
	host := base.Hostname()
	port := 80
	if p := base.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			port = n
		}
	}
	name := desc.Device.FriendlyName
	if name == "" {
		name = desc.Device.ModelName
	}
	return &IGDConfig{
		Kind:         best.kind,
		Address:      host,
		Port:         port,
		Location:     location,
		ControlURL:   best.ctrl,
		ServiceType:  best.svcType,
		ModelName:    desc.Device.ModelName,
		Manufacturer: desc.Device.Manufacturer,
		UDN:          desc.Device.UDN,
		Name:         name,
		Timeout:      timeout,

		IPv6FirewallControlURL:  v6Ctrl,
		IPv6FirewallServiceType: v6Svc,
	}, &desc, nil
}

func resolveURL(base *url.URL, ref string) string {
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ref
	}
	return base.ResolveReference(u).String()
}
