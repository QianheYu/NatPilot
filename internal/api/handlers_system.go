package api

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ekko-studio/natpilot/internal/bus"
	"github.com/ekko-studio/natpilot/internal/config"
	"github.com/ekko-studio/natpilot/internal/logging"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/netutil"
	"github.com/ekko-studio/natpilot/internal/scanner"
	"github.com/ekko-studio/natpilot/internal/store"
)

// storePortFilter builds a discovered-port filter from query parameters.
func storePortFilter(r *http.Request) store.PortStatusFilter {
	q := r.URL.Query()
	f := store.PortStatusFilter{
		Host:       strings.TrimSpace(q.Get("host")),
		Query:      strings.TrimSpace(q.Get("q")),
		TargetKind: strings.TrimSpace(q.Get("target_kind")),
		Sort:       q.Get("sort"),
		Order:      q.Get("order"),
		Limit:      atoiDefault(q.Get("limit"), 500),
		Offset:     atoiDefault(q.Get("offset"), 0),
		ScanID:     int64(atoiDefault(q.Get("scan_id"), 0)),
	}
	if v := strings.TrimSpace(q.Get("status")); v != "" {
		f.Statuses = splitCSV(v)
	}
	if v, ok := queryBool(r, "open_only"); ok {
		f.OnlyOpen = v
	}
	if v, ok := queryBool(r, "added"); ok {
		f.OnlyAdded = &v
	}
	if f.Limit <= 0 || f.Limit > 5000 {
		f.Limit = 500
	}
	return f
}

// ---- gateways & mappings ----

func (s *Server) handleListGateways(w http.ResponseWriter, r *http.Request) {
	gws := s.app.Mapper.GatewayList()
	pref := s.app.Mapper.PreferredKey()
	writeJSON(w, http.StatusOK, map[string]any{
		"gateways":         gws,
		"total":            len(gws),
		"preferred":        pref,
		"last_discover":    s.app.Mapper.LastDiscover(),
		"last_error":       s.app.Mapper.LastError(),
		"configured_order": s.app.Config.Mapper.Protocols,
	})
}

func (s *Server) handleDiscover(w http.ResponseWriter, r *http.Request) {
	gws, err := s.app.Mapper.Discover(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	s.audit(r, "gateway.discover", fmt.Sprintf("%d gateways", len(gws)), "", true)
	var ip string
	if s.app.Config.Mapper.ExternalIPCheck {
		ip, _ = s.app.Mapper.ExternalIP(r.Context())
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "gateways": gws, "external_ip": ip})
}

func (s *Server) handleGatewayPreferred(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid gateway id")
		return
	}
	gw, err := s.app.Store.GetGateway(id)
	if err != nil || gw == nil {
		writeError(w, http.StatusNotFound, "gateway not found")
		return
	}
	key := fmt.Sprintf("%s|%s|%d", gw.Kind, gw.Address, gw.Port)
	s.app.Mapper.SetPreferred(key)
	s.audit(r, "gateway.preferred", key, "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "preferred": key})
}

func (s *Server) handleGatewayEnable(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid gateway id")
		return
	}
	var req struct {
		Enabled *bool `json:"enabled"`
	}
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if err := s.app.Store.SetGatewayEnabled(id, enabled); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "enabled": enabled})
}

func (s *Server) handleGatewayDelete(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid gateway id")
		return
	}
	if err := s.app.Store.DeleteGateway(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

func (s *Server) handleGatewayMappings(w http.ResponseWriter, r *http.Request) {
	list, err := s.app.Mapper.ListGatewayMappings(r.Context())
	if err != nil {
		writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"mappings": list, "total": len(list)})
}

func (s *Server) handleExternalIP(w http.ResponseWriter, r *http.Request) {
	ip, err := s.app.Mapper.ExternalIP(r.Context())
	out := map[string]any{"ok": err == nil, "external_ip": ip}
	if err != nil {
		out["error"] = err.Error()
	}
	if s.app.Config.Mapper.ExternalIPCheck {
		if httpIP, herr := netutil.ExternalIPHTTP(r.Context(), nil); herr == nil {
			out["http_external_ip"] = httpIP
		}
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleReconcile(w http.ResponseWriter, r *http.Request) {
	sum, err := s.app.Mapper.Reconcile(r.Context())
	s.audit(r, "mapper.reconcile", sum.String(), "", err == nil)
	out := map[string]any{"ok": err == nil, "summary": sum}
	if err != nil {
		out["error"] = err.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) handleCleanup(w http.ResponseWriter, r *http.Request) {
	rules, err := s.app.Store.AllRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	removed := 0
	records := s.app.Mapper.Records()
	for _, rule := range rules {
		if rule.Enabled {
			continue
		}
		if rec, ok := records[rule.ID]; ok && rec.Status == model.MapStatusActive {
			if err := s.app.Mapper.RemoveMapping(r.Context(), rule); err == nil {
				removed++
			}
		}
	}
	_ = s.app.Store.ReconcilePortStatuses()
	s.audit(r, "mapper.cleanup", fmt.Sprintf("%d mappings removed", removed), "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "removed": removed})
}

// ---- hosts ----

func (s *Server) handleListHosts(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 500)
	hosts, err := s.app.Store.ListHosts(limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"hosts": hosts, "total": len(hosts)})
}

// hostIdentityRequest is the body of PATCH /api/hosts/{id}. Every field is
// optional; only the ones present are applied.
type hostIdentityRequest struct {
	Hostname *string   `json:"hostname"`
	Note     *string   `json:"note"`
	Tags     *[]string `json:"tags"`
}

// handlePatchHost lets a user correct the name/note/tags that stick to a MAC.
//
// A user-set hostname must survive later reverse-DNS lookups, so discovery only
// fills an empty hostname (see hostnameIfUnknown) and UpsertHost keeps an empty
// observation from erasing it. The MAC itself is not editable: host-bound
// policies follow it.
func (s *Server) handlePatchHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	host, err := s.app.Store.GetHostByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	var req hostIdentityRequest
	if err := decodeJSONLoose(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	hostname, note, tags := host.Hostname, host.Note, []string(host.Tags)
	if req.Hostname != nil {
		hostname = strings.TrimSpace(*req.Hostname)
	}
	if req.Note != nil {
		note = *req.Note
	}
	if req.Tags != nil {
		tags = *req.Tags
	}
	if err := s.app.Store.SetHostIdentity(id, hostname, note, tags); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "host.identity", fmt.Sprintf("host #%d", id), hostname, true)
	fresh, err := s.app.Store.GetHostByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"host": fresh})
}

// hostnameIfUnknown resolves a reverse-DNS name only when the host has no name
// yet. A PTR record is a guess; a user-corrected name (PATCH /api/hosts/{id})
// or a name learned earlier must never be overwritten by it.
func (s *Server) hostnameIfUnknown(ctx context.Context, ip, mac string) string {
	if h, err := s.app.Store.GetHostByIP(ip); err == nil && h != nil && strings.TrimSpace(h.Hostname) != "" {
		return ""
	}
	if strings.TrimSpace(mac) != "" {
		if h, err := s.app.Store.GetHostByMAC(mac); err == nil && h != nil && strings.TrimSpace(h.Hostname) != "" {
			return ""
		}
	}
	return netutil.ReverseDNS(ctx, ip)
}

// handleHostScan discovers hosts on the LAN. The request body is optional:
//
//	{}                                             passive: read the OS neighbour table
//	{"iface":"以太网"}                              passive, filtered to one interface
//	{"mode":"passive","iface":"WLAN"}               same, explicitly requested
//	{"iface":"WLAN|fe80::1%12"}                     active sweep of that interface's subnet
//	{"mode":"subnet","iface":"WLAN|fe80::1%12"}     same, explicitly requested
//	{"cidr":"192.168.1.0/24","iface":"以太网"}       active sweep of an explicit subnet
//	{"mode":"cidr","cidr":"192.168.1.0/24"}         active sweep, cidr required
//
// Resolution order: mode "passive", then a non-empty cidr (or mode "cidr"),
// then the selected interface's own subnet, then the passive neighbour pass.
// Leaving cidr empty while selecting an interface therefore sweeps that
// interface's subnet; omitting both keeps the old passive behaviour.
//
// Passive discovery can only see peers the OS already talked to; the active
// sweep is what makes "scan this subnet on this NIC" work, and it also forces the
// ARP entries that give each host its MAC (host-bound policies key on the MAC).
// hostScanParams is one sweep: an explicit CIDR, an interface (its own subnet),
// or neither (passive neighbour discovery).
type hostScanParams struct {
	Mode      string
	CIDR      string
	Iface     string
	Ports     string
	TimeoutMS int
	MaxHosts  int
}

// hostScanResult is the per-target outcome. `error` is set when that one target
// failed, which must not abort the other targets of a multi-target request.
type hostScanResult struct {
	Mode       string           `json:"mode"`
	CIDR       string           `json:"cidr"`
	Iface      string           `json:"iface"`
	Alive      int              `json:"alive"`
	Skipped    int              `json:"skipped"`
	Discovered int              `json:"discovered"`
	Hosts      []map[string]any `json:"hosts"`
	Error      string           `json:"error,omitempty"`
}

// runHostScan executes ONE host-scan target. It is shared by the HTTP handler and
// the automatic scan loop so both behave identically.
func (s *Server) runHostScan(ctx context.Context, p hostScanParams) (hostScanResult, error) {
	ifaces, _ := netutil.Interfaces()
	local, _ := netutil.LocalAddresses()
	chosen, hasIface := scanner.PickInterface(ifaces, p.Iface)
	if strings.TrimSpace(p.Iface) != "" && !hasIface {
		return hostScanResult{}, fmt.Errorf("未找到网卡 %q", p.Iface)
	}
	ifaceName := ""
	if hasIface {
		ifaceName = chosen.Name
	}
	kind, cidrOut, err := resolveScanMode(p.CIDR, p.Iface, p.Mode, chosen, hasIface)
	if err != nil {
		return hostScanResult{}, err
	}
	res := hostScanResult{Mode: kind, CIDR: cidrOut, Iface: ifaceName, Hosts: []map[string]any{}}

	if kind != scanModePassive {
		ports, perr := scanner.ParseProbePorts(p.Ports)
		if perr != nil {
			return hostScanResult{}, perr
		}
		probe := scanner.HostProbe{CIDR: cidrOut, Ports: ports, MaxHosts: p.MaxHosts}
		if hasIface {
			// 绑定源地址就等于选定网卡：多网卡机器上不会走到默认路由那块网卡。
			if bindSourceFamily(chosen, cidrOut) {
				probe.SourceIP = chosen.IP
			}
			if chosen.IsV6() {
				probe.SourceZone = chosen.Zone
				probe.TargetZone = chosen.Zone
			}
		}
		if p.TimeoutMS > 0 {
			probe.Timeout = time.Duration(p.TimeoutMS) * time.Millisecond
		}
		alive, skipped, serr := scanner.ProbeCIDR(ctx, probe)
		if serr != nil {
			return hostScanResult{}, serr
		}
		res.Alive, res.Skipped = len(alive), skipped
		macOf := map[string]netutil.Neighbor{}
		if neighbors, nerr := netutil.DiscoverNeighbors(ctx); nerr == nil {
			for _, n := range neighbors {
				macOf[n.IP] = n
			}
		}
		for _, ip := range alive {
			h := &model.Host{
				IP: ip, IsLocal: local[ip], Kind: classifyHost(ip, ifaces),
				Source: "scan", Interface: ifaceName,
			}
			if n, ok := macOf[ip]; ok {
				h.MAC = n.MAC
				h.Vendor = n.Vendor
				if h.Interface == "" {
					h.Interface = n.Iface
				}
			}
			h.Addresses = []model.HostAddress{hostAddressFor(ip, h.Interface, "scan")}
			h.Hostname = s.hostnameIfUnknown(ctx, ip, h.MAC)
			if err := s.app.Store.UpsertHost(h); err == nil {
				res.Discovered++
			}
			res.Hosts = append(res.Hosts, map[string]any{
				"ip": h.IP, "mac": h.MAC, "vendor": h.Vendor,
				"hostname": h.Hostname, "iface": h.Interface, "is_local": h.IsLocal,
			})
		}
		s.app.Bus.PublishType("hosts", map[string]any{"count": res.Discovered})
		return res, nil
	}

	neighbors, _ := netutil.DiscoverNeighbors(ctx)
	for _, n := range neighbors {
		if hasIface && !strings.EqualFold(n.Iface, chosen.Name) {
			continue
		}
		src := "arp"
		if strings.Contains(n.IP, ":") {
			src = "ndp"
		}
		h := &model.Host{
			IP: n.IP, MAC: n.MAC, Vendor: n.Vendor, Interface: n.Iface,
			IsLocal: local[n.IP], Kind: classifyHost(n.IP, ifaces), Source: src,
			Hostname: s.hostnameIfUnknown(ctx, n.IP, n.MAC),
		}
		h.Addresses = []model.HostAddress{hostAddressFor(n.IP, n.Iface, src)}
		if err := s.app.Store.UpsertHost(h); err == nil {
			res.Discovered++
		}
	}
	for _, i := range ifaces {
		if hasIface && !strings.EqualFold(i.Name, chosen.Name) {
			continue
		}
		h := &model.Host{
			IP: i.IP, MAC: i.MAC, Interface: i.Name, IsLocal: true,
			Kind: "self", Source: "interface", Hostname: hostname(),
		}
		h.Addresses = []model.HostAddress{hostAddressFor(i.IP, i.Zone, "interface")}
		_ = s.app.Store.UpsertHost(h)
	}
	s.app.Bus.PublishType("hosts", map[string]any{"count": res.Discovered})
	return res, nil
}

// hostScanTargetReq is one entry of the optional `targets` array: scan the
// subnet of an interface, or an explicit CIDR. Several targets may be combined.
type hostScanTargetReq struct {
	Kind  string `json:"kind"`
	Iface string `json:"iface"`
	CIDR  string `json:"cidr"`
}

func (s *Server) handleHostScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		CIDR      string              `json:"cidr"`
		Iface     string              `json:"iface"`
		Mode      string              `json:"mode"`
		Ports     string              `json:"ports"`
		TimeoutMS int                 `json:"timeout_ms"`
		MaxHosts  int                 `json:"max_hosts"`
		Targets   []hostScanTargetReq `json:"targets"`
	}
	// An empty body keeps the old "discover what the OS already knows" behaviour.
	_ = decodeJSONLoose(r, &req)

	params := make([]hostScanParams, 0, len(req.Targets))
	if len(req.Targets) > 0 {
		if len(req.Targets) > 16 {
			writeError(w, http.StatusBadRequest, "一次最多 16 个扫描目标")
			return
		}
		for _, t := range req.Targets {
			p := hostScanParams{
				Ports: req.Ports, TimeoutMS: req.TimeoutMS, MaxHosts: req.MaxHosts,
				CIDR: strings.TrimSpace(t.CIDR), Iface: strings.TrimSpace(t.Iface),
			}
			switch strings.ToLower(strings.TrimSpace(t.Kind)) {
			case "iface", "subnet", "interface":
				p.Mode = scanModeSubnet
			case "cidr", "subnet-cidr", "":
				p.Mode = scanModeCIDR
			default:
				writeError(w, http.StatusBadRequest, fmt.Sprintf("未知目标类型 %q（iface 或 cidr）", t.Kind))
				return
			}
			params = append(params, p)
		}
	} else {
		params = append(params, hostScanParams{
			Mode: req.Mode, CIDR: req.CIDR, Iface: req.Iface,
			Ports: req.Ports, TimeoutMS: req.TimeoutMS, MaxHosts: req.MaxHosts,
		})
	}

	results := make([]hostScanResult, 0, len(params))
	valid, firstErr := 0, error(nil)
	for _, p := range params {
		res, err := s.runHostScan(r.Context(), p)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			results = append(results, hostScanResult{
				Mode: p.Mode, CIDR: p.CIDR, Iface: p.Iface,
				Hosts: []map[string]any{}, Error: err.Error(),
			})
			continue
		}
		valid++
		results = append(results, res)
	}
	if valid == 0 && firstErr != nil {
		writeError(w, http.StatusBadRequest, firstErr.Error())
		return
	}

	alive, discovered, skipped := 0, 0, 0
	hosts := []map[string]any{}
	for _, res := range results {
		alive += res.Alive
		discovered += res.Discovered
		skipped += res.Skipped
		hosts = append(hosts, res.Hosts...)
	}
	first := results[0]
	out := map[string]any{
		"ok": true, "mode": first.Mode, "discovered": discovered, "alive": alive,
		"cidr": first.CIDR, "iface": first.Iface, "skipped": skipped, "hosts": hosts,
	}
	if len(results) > 1 {
		out["results"] = results
	}
	s.audit(r, "host.discover", fmt.Sprintf("%d target(s): %d alive, %d discovered",
		len(results), alive, discovered), "", true)
	writeJSON(w, http.StatusOK, out)
}

// Scan modes reported in the host-scan response and accepted in the request.
const (
	scanModePassive = "passive"
	scanModeSubnet  = "subnet"
	scanModeCIDR    = "cidr"
)

// resolveScanMode turns the user's (cidr, iface, mode) triple into the scan to
// run. It is pure so the whole matrix can be table-tested without a network.
//
// Rules, in order:
//   - mode "passive" always wins and ignores cidr/iface.
//   - a non-empty cidr (or mode "cidr") sweeps that cidr; mode "cidr" without
//     one is an error.
//   - cidr empty with an interface (or mode "subnet") sweeps the interface's
//     own subnet, taken from Network and falling back to IPNet.
//   - nothing given falls back to the passive neighbour pass.
//
// kind is one of scanModePassive, scanModeSubnet or scanModeCIDR. Errors are
// meant to be returned to the caller as HTTP 400.
func resolveScanMode(cidr, iface, mode string, ifc netutil.Iface, hasIface bool) (kind, cidrOut string, err error) {
	cidr = strings.TrimSpace(cidr)
	iface = strings.TrimSpace(iface)
	mode = strings.ToLower(strings.TrimSpace(mode))

	switch mode {
	case "", "auto", scanModePassive, scanModeSubnet, scanModeCIDR:
	default:
		return "", "", fmt.Errorf("invalid mode %q (want auto, passive, subnet or cidr)", mode)
	}
	if iface != "" && !hasIface {
		return "", "", fmt.Errorf("interface %q not found", iface)
	}
	if mode == scanModePassive {
		return scanModePassive, "", nil
	}
	if cidr != "" {
		return scanModeCIDR, cidr, nil
	}
	if mode == scanModeCIDR {
		return "", "", fmt.Errorf("mode %q requires a cidr", scanModeCIDR)
	}
	if hasIface {
		subnet := strings.TrimSpace(ifc.Network)
		if subnet == "" {
			subnet = strings.TrimSpace(ifc.IPNet)
		}
		name := orDefault(ifc.Name, iface)
		if subnet == "" {
			return "", "", fmt.Errorf("interface %q has no usable subnet (both network and ip_net are empty)", name)
		}
		if _, _, perr := net.ParseCIDR(subnet); perr != nil {
			return "", "", fmt.Errorf("interface %q has an invalid subnet %q", name, subnet)
		}
		return scanModeSubnet, subnet, nil
	}
	if mode == scanModeSubnet {
		return "", "", fmt.Errorf("mode %q requires an interface", scanModeSubnet)
	}
	return scanModePassive, "", nil
}

// bindSourceFamily reports whether ifc can source a sweep of cidr. A CIDR of the
// other address family is left unbound (the OS chooses the source) instead of
// pinning a source address whose family does not match the destination, which
// would make every connect attempt fail at bind time. An unparseable CIDR is
// handled by ProbeCIDR, which reports it.
func bindSourceFamily(ifc netutil.Iface, cidr string) bool {
	ip, _, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	return (ip.To4() == nil) == ifc.IsV6()
}

func hostname() string {
	h, err := netutil.PrimaryIPv4()
	if err != nil {
		return ""
	}
	_ = h
	return ""
}

func classifyHost(ip string, ifaces []netutil.Iface) string {
	for _, i := range ifaces {
		if i.Gateway == ip {
			return "gateway"
		}
	}
	return "peer"
}

// handleListHostPolicies returns the policies that follow one host.
func (s *Server) handleListHostPolicies(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	host, err := s.app.Store.GetHostByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	policies, err := s.app.Store.ListPoliciesByHost(host.MAC)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"host":     host,
		"policies": policies,
		"total":    len(policies),
	})
}

// hostPolicyRequest is the body of POST /api/hosts/{id}/policies. Everything is
// optional except that the resulting rule set has to mean something, so sane
// defaults fill the gaps (watch every TCP port unless told otherwise).
// hostPolicyRequest carries the whole policy payload, not a reduced subset:
// the host entry must be able to express everything the 策略与监控 editor can,
// otherwise the same policy means different things depending on where it was
// created. Only the host binding itself (MAC) is implied by the URL.
type hostPolicyRequest struct {
	Name          string   `json:"name"`
	Description   string   `json:"description"`
	MatchTarget   string   `json:"match_target_kind"`
	MACPatterns   string   `json:"mac_patterns"`
	PortRanges    string   `json:"port_ranges"`
	ExcludeRanges string   `json:"exclude_ranges"`
	Protocol      string   `json:"protocol"`
	AutoAction    string   `json:"auto_action"`
	AutoAdd       bool     `json:"auto_add"`
	MinPort       int      `json:"min_port"`
	MaxPort       int      `json:"max_port"`
	RequireName   string   `json:"require_name"`
	Tags          []string `json:"tags"`
	Note          string   `json:"note"`
	Priority      int      `json:"priority"`
	Enabled       *bool    `json:"enabled"`
}

// handleCreateHostPolicy binds a new policy to a host by its MAC address, so the
// policy keeps applying after DHCP changes the host's IP.
func (s *Server) handleCreateHostPolicy(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	host, err := s.app.Store.GetHostByID(id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if host == nil {
		writeError(w, http.StatusNotFound, "host not found")
		return
	}
	if strings.TrimSpace(host.MAC) == "" {
		writeError(w, http.StatusBadRequest,
			"该主机没有 MAC 地址，无法建立基于 MAC 的监控；请先执行一次主机发现")
		return
	}

	var req hostPolicyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	label := host.Hostname
	if strings.TrimSpace(label) == "" {
		label = host.IP
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = "监控 " + label
	}
	action := strings.TrimSpace(req.AutoAction)
	if action == "" {
		if req.AutoAdd {
			action = model.ActionAdd
		} else {
			action = model.ActionWatch
		}
	}

	// 匹配范围：主机策略默认就是"按这台主机的 MAC 匹配"。既写 MAC（单机绑定，
	// 匹配时优先使用），也把同一个 MAC 写进 MACPatterns（"MAC 范围"字段），
	// 这样在策略页编辑时匹配规则不是空的、与手工创建的 mac 类型策略一致。
	matchKind := strings.TrimSpace(req.MatchTarget)
	if matchKind == "" {
		matchKind = model.ScanTargetMAC
	}
	macPatterns := strings.TrimSpace(req.MACPatterns)
	if macPatterns == "" && matchKind == model.ScanTargetMAC {
		macPatterns = host.MAC
	}

	p := &model.Policy{
		Name:          name,
		Description:   req.Description,
		Enabled:       true,
		MatchTarget:   matchKind,
		MACPatterns:   macPatterns,
		PortRanges:    strings.TrimSpace(req.PortRanges),
		ExcludeRanges: req.ExcludeRanges,
		Protocol:      orDefault(strings.TrimSpace(req.Protocol), "any"),
		AutoAction:    action,
		AutoAdd:       action == model.ActionAdd || req.AutoAdd,
		MinPort:       req.MinPort,
		MaxPort:       req.MaxPort,
		RequireName:   req.RequireName,
		Tags:          model.StringList(req.Tags),
		Note:          req.Note,
		Priority:      req.Priority,
		MAC:           host.MAC,
	}
	if req.Enabled != nil {
		p.Enabled = *req.Enabled
	}
	if err := validatePolicy(p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	newID, err := s.app.Store.CreatePolicy(p)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "host.policy.create", fmt.Sprintf("host #%d (%s) policy #%d", host.ID, host.MAC, newID), "", true)
	writeJSON(w, http.StatusCreated, map[string]any{"ok": true, "id": newID, "policy": p})
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

func (s *Server) handleDeleteHost(w http.ResponseWriter, r *http.Request) {
	id, err := pathInt(r, "id")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid host id")
		return
	}
	if err := s.app.Store.DeleteHost(id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, map[string]any{"deleted": id})
}

// ---- events / SSE ----

func (s *Server) handleListEvents(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := store.EventFilter{
		Level:    strings.TrimSpace(q.Get("level")),
		Category: strings.TrimSpace(q.Get("category")),
		Query:    strings.TrimSpace(q.Get("q")),
		Limit:    atoiDefault(q.Get("limit"), 200),
		Offset:   atoiDefault(q.Get("offset"), 0),
	}
	if v, ok := queryBool(r, "unread"); ok {
		f.Unread = v
	}
	events, total, err := s.app.Store.ListEvents(f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	unread, _ := s.app.Store.CountUnreadEvents()
	writeJSON(w, http.StatusOK, map[string]any{"events": events, "total": total, "unread": unread})
}

func (s *Server) handleEventsRead(w http.ResponseWriter, r *http.Request) {
	var req struct {
		IDs []int64 `json:"ids"`
	}
	_ = decodeJSONLoose(r, &req)
	if err := s.app.Store.MarkEventsRead(req.IDs); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeOK(w, nil)
}

func (s *Server) handleClearEvents(w http.ResponseWriter, r *http.Request) {
	keep := atoiDefault(r.URL.Query().Get("keep"), 0)
	n, err := s.app.Store.ClearEvents(keep)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "deleted": n})
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request) {
	limit := atoiDefault(r.URL.Query().Get("limit"), 100)
	offset := atoiDefault(r.URL.Query().Get("offset"), 0)
	list, total, err := s.app.Store.ListAudit(limit, offset)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"audit": list, "total": total})
}

// handleEventStream streams server events as SSE.
func (s *Server) handleEventStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming not supported by this server")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.app.Bus.Subscribe(128)
	defer cancel()

	hello, _ := json.Marshal(map[string]any{"ok": true, "version": s.app.Version})
	fmt.Fprintf(w, "event: hello\ndata: %s\n\n", hello)
	flusher.Flush()

	keep := time.NewTicker(20 * time.Second)
	defer keep.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case ev, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev.Type, mustJSON(ev.Data))
			flusher.Flush()
		case <-keep.C:
			fmt.Fprintf(w, ": keep-alive %d\n\n", time.Now().Unix())
			flusher.Flush()
		}
	}
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// ---- dashboard & stats ----

func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.app.buildStats(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (s *Server) handleDashboard(w http.ResponseWriter, r *http.Request) {
	st, err := s.app.buildStats(r)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	gws := s.app.Mapper.GatewayList()
	rules, _, _ := s.app.Store.ListRules(store.RuleFilter{Limit: 8, Sort: "updated"})
	openPorts, _, _ := s.app.Store.ListPorts(store.PortStatusFilter{Statuses: []string{model.StatusNotAdded, model.StatusOpen}, Limit: 8})
	events, _, _ := s.app.Store.ListEvents(store.EventFilter{Limit: 12})
	writeJSON(w, http.StatusOK, map[string]any{
		"stats":         st,
		"gateways":      gws,
		"recent_rules":  s.enrichRules(rules, false),
		"pending_ports": openPorts,
		"recent_events": events,
		"monitor":       s.app.Scanner.MonitorStatus(),
		"external_ip":   s.app.lastExternalIP(),
	})
}

func (a *App) buildStats(r *http.Request) (model.Stats, error) {
	var st model.Stats
	rules, _, err := a.Store.ListRules(store.RuleFilter{Limit: 100000})
	if err != nil {
		return st, err
	}
	records, _ := a.Store.MappingsByRule()
	conflicts := a.Mapper.Conflicts()
	st.Rules = len(rules)
	st.ByProtocol = map[string]int{}
	st.ByStatus = map[string]int{}
	st.ByTag = map[string]int{}
	st.ByProvider = map[string]int{}
	st.RulesActive = 0
	for _, rule := range rules {
		st.ByProtocol[rule.Protocol]++
		if rule.Enabled {
			st.Enabled++
		}
		for _, t := range rule.Tags {
			st.ByTag[t]++
		}
		rec, ok := records[rule.ID]
		status := "unmapped"
		if !rule.Enabled {
			status = "disabled"
		} else if ok {
			status = rec.Status
			if rec.ProviderName != "" {
				st.ByProvider[shortProvider(rec.ProviderName)]++
			}
		}
		if _, bad := conflicts[rule.ID]; bad && status != "active" {
			status = "conflict"
			st.Conflicts++
		}
		st.ByStatus[status]++
		switch status {
		case model.MapStatusActive:
			st.Mapped++
		case model.MapStatusFailed:
			st.Failures++
		}
	}
	counts, _ := a.Store.CountPortsByStatus()
	st.PortsOpen = counts[model.StatusOpen] + counts[model.StatusNotAdded] + counts[model.StatusAdded]
	st.GoneSources = counts[model.StatusSourceGone]
	st.Hosts, _ = a.Store.CountHosts()
	gws := a.Mapper.GatewayList()
	st.Gateways = len(gws)
	st.ScansToday, _ = a.Store.CountScansSince(time.Now().UTC().Truncate(24 * time.Hour).Format(time.RFC3339Nano))
	scheds, _ := a.Store.ListSchedules()
	st.Schedules = len(scheds)
	scans, _, _ := a.Store.ListScans(5, 0)
	st.RecentScans = scans
	st.MappingsTot = len(records)
	st.UpdatedAt = model.Time{Time: time.Now()}
	return st, nil
}

func shortProvider(key string) string {
	if i := strings.Index(key, "|"); i > 0 {
		return key[:i]
	}
	return key
}

func (a *App) lastExternalIP() string {
	gws := a.Mapper.GatewayList()
	for _, g := range gws {
		if g.ExternalIP != "" {
			return g.ExternalIP
		}
	}
	return ""
}

// ---- system info & settings ----

func (s *Server) handleSystemInfo(w http.ResponseWriter, r *http.Request) {
	ifaces, _ := netutil.Interfaces()
	gw, _ := netutil.DefaultGateway()
	primary, _ := netutil.PrimaryIPv4()
	users, _ := s.app.Store.CountUsers()
	writeJSON(w, http.StatusOK, map[string]any{
		"version":     s.app.Version,
		"mode":        s.app.Config.EffectiveMode(),
		"uptime_s":    int(time.Since(s.app.StartedAt).Seconds()),
		"started_at":  s.app.StartedAt,
		"primary_ip":  primary,
		"gateway":     gw,
		"interfaces":  ifaces,
		"db_path":     s.app.Store.Path(),
		"data_dir":    s.app.Config.App.DataDir,
		"users":       users,
		"auth":        s.app.Auth.Enabled(),
		"log_level":   s.app.Config.Log.Level,
		"protocols":   s.app.Config.Mapper.Protocols,
		"server_time": time.Now().UTC(),
	})
}

func (s *Server) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	cfg := s.app.Config.RedactedMap()
	writeJSON(w, http.StatusOK, map[string]any{
		"config":    cfg,
		"overrides": s.app.Config.Overrides(),
	})
}

func (s *Server) handlePutSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := decodeJSONLoose(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// Flatten nested keys into dotted override keys.
	flat := map[string]string{}
	flattenSettings("", raw, flat)
	if err := s.validateSettingOverrides(flat); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	applied, restartRequired, unsupported, cleared, err := s.applySettingOverrides(flat)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "settings.update", strings.Join(applied, ","), "", true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"applied":          applied,
		"restart_required": restartRequired,
		"unsupported":      unsupported,
		"cleared":          cleared,
	})
}

// restartOnlySettings change the security or network boundary of the process.
// They are persisted and take effect on the next start, but are never hot-applied:
// otherwise one PATCH could silently turn a live, internet-facing server into an
// unauthenticated desktop console (or move its listening port) without anybody
// restarting it.
var restartOnlySettings = map[string]bool{
	"http.host":            true,
	"http.port":            true,
	"http.mode":            true,
	"auth.enabled":         true,
	"auth.require_for_web": true,
}

// settingValueToString converts a JSON value sent by the web console into the
// textual form the settings table stores. The console submits real booleans and
// numbers, so decoding straight into map[string]string rejected every save.
func settingValueToString(v any) (string, bool) {
	switch val := v.(type) {
	case nil:
		return "", true
	case string:
		return val, true
	case bool:
		return strconv.FormatBool(val), true
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64), true
	case int:
		return strconv.Itoa(val), true
	case int64:
		return strconv.FormatInt(val, 10), true
	case []string:
		return strings.Join(val, ","), true
	case []any:
		parts := make([]string, 0, len(val))
		for _, item := range val {
			str, ok := settingValueToString(item)
			if !ok {
				return "", false
			}
			parts = append(parts, str)
		}
		return strings.Join(parts, ","), true
	}
	return "", false
}

// validateSetting rejects values that would corrupt the stored configuration and
// otherwise only surface as a broken start-up much later.
func validateSetting(k, v string) error {
	switch k {
	case "http.mode":
		switch strings.ToLower(strings.TrimSpace(v)) {
		case config.ModeAuto, config.ModeWeb, config.ModeServer, config.ModeDesktop:
		default:
			return fmt.Errorf("http.mode must be auto, web, server or desktop")
		}
	case "http.port":
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || n < 0 || n > 65535 {
			return fmt.Errorf("http.port must be an integer between 0 and 65535")
		}
	}
	return nil
}

// validateSettingOverrides checks every allowed override against a copy of the live
// configuration. Doing it on a clone keeps one source of truth (Config.Validate) and
// rejects an enum/host/TLS mistake with 400 now, instead of persisting a value that
// would make the next start fail.
func (s *Server) validateSettingOverrides(kv map[string]string) error {
	for _, k := range sortedKeys(kv) {
		if isSecretKey(k) || removedSettings[k] || !allowedSetting(k) {
			continue
		}
		// Cheap per-key check first so the error names the offending key.
		if err := validateSetting(k, kv[k]); err != nil {
			return err
		}
	}
	candidate := s.app.Config.Clone()
	for _, k := range sortedKeys(kv) {
		if isSecretKey(k) || removedSettings[k] || !allowedSetting(k) {
			continue
		}
		candidate.SetOverride(k, kv[k])
	}
	return candidate.Validate()
}

func sortedKeys(kv map[string]string) []string {
	keys := make([]string, 0, len(kv))
	for k := range kv {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applySettingOverrides persists the given overrides and hot-applies only the ones
// that are safe to change at runtime, reporting the rest as requiring a restart.
func (s *Server) applySettingOverrides(kv map[string]string) (applied, restartRequired, unsupported, cleared []string, err error) {
	// Never return nil slices: they marshal to JSON null and the web console reads
	// response.applied.length.
	applied = []string{}
	restartRequired = []string{}
	unsupported = []string{}
	for _, k := range sortedKeys(kv) {
		v := kv[k]
		if isSecretKey(k) {
			continue
		}
		if removedSettings[k] {
			// Known key that this build no longer honours: say so instead of
			// pretending it was applied.
			unsupported = append(unsupported, k)
			continue
		}
		if !allowedSetting(k) {
			continue
		}
		// 空值 = "清除这条覆盖项"，回落到配置文件/环境变量/默认值。
		// 没有这个出口，一次误保存就会把某个设置永久钉死在数据库里。
		if strings.TrimSpace(v) == "" {
			if serr := s.app.Store.DeleteSetting("config." + k); serr != nil {
				return applied, restartRequired, unsupported, cleared, serr
			}
			cleared = append(cleared, k)
			continue
		}
		if restartOnlySettings[k] {
			if serr := s.app.Store.SetSetting("config."+k, v); serr != nil {
				return applied, restartRequired, unsupported, cleared, serr
			}
			restartRequired = append(restartRequired, k)
			continue
		}
		// Apply first: a key the configuration cannot understand must not be
		// persisted, or it would be replayed on every start without taking effect.
		if !s.app.Config.SetOverride(k, v) {
			unsupported = append(unsupported, k)
			continue
		}
		if serr := s.app.Store.SetSetting("config."+k, v); serr != nil {
			return applied, restartRequired, unsupported, cleared, serr
		}
		// The log level is the one setting that must take effect immediately: the
		// console offers "off" as the default, and toggling it has to stop (or
		// start) both stderr output and the persisted event stream right away.
		if k == "log.level" {
			logging.Init(s.app.Config.Log.Level, s.app.Config.Log.Format)
		}
		applied = append(applied, k)
	}
	return applied, restartRequired, unsupported, cleared, nil
}

func flattenSettings(prefix string, in map[string]any, out map[string]string) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}
		if nested, ok := v.(map[string]any); ok {
			flattenSettings(key, nested, out)
			continue
		}
		if str, ok := settingValueToString(v); ok {
			out[key] = str
		}
	}
}

// allowedSettingKeys lists every configuration key the settings endpoints may
// write. A plain key list keeps the table readable and gofmt-stable; the set is
// built once below.
var allowedSettingKeys = newKeySet(
	"http.host",
	"http.port",
	"http.mode",
	"http.base_path",
	"http.trust_proxy",
	"http.tls.enabled",
	"http.tls.cert_file",
	"http.tls.key_file",

	"scanner.max_concurrency",
	"scanner.timeout_ms",
	"scanner.default_ports",
	"scanner.grab_banners",
	"scanner.auto_monitor_local",
	"scanner.auto_interval_seconds",
	"scanner.auto_apply_policy",
	"scanner.auto_add_open",
	"scanner.auto_add_ports",
	"scanner.enable_lan_scan",
	"scanner.max_hosts_per_scan",
	"scanner.max_ports_per_scan",
	"scanner.rate_limit_pps",
	"scanner.auto_local_policy",

	"mapper.enabled",
	"mapper.dry_run",
	"mapper.default_lease_seconds",
	"mapper.max_lease_seconds",
	"mapper.default_protocol",
	"mapper.renew_seconds_before_expiry",
	"mapper.discover_interval_seconds",
	"mapper.reconcile_interval_seconds",
	"mapper.cleanup_deleted_rules",
	"mapper.conflict_policy",
	"mapper.external_ip_check",
	"mapper.allow_external_port_range",

	"log.level",
	"log.format",
	"log.max_entries",
	"log.retention_days",

	// 保留策略（日志审计 / 扫描任务）
	"retention.events_days",
	"retention.events_max",
	"retention.scans_days",
	"retention.scans_max",

	"auth.enabled",
	"auth.require_for_web",
	"auth.session_ttl_hours",
	"auth.lockout_attempts",
	"auth.lockout_minutes",
	"auth.api_token_ttl_hours",
)

func newKeySet(keys ...string) map[string]bool {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[k] = true
	}
	return set
}

// removedSettings are keys an older build accepted but this one no longer honours.
// They are reported back as unsupported instead of being dropped silently, so a
// console or config file written for the old version does not look like it worked.
var removedSettings = map[string]bool{
	"auth.allow_registration": true,
}

func allowedSetting(k string) bool {
	if allowedSettingKeys[k] {
		return true
	}
	// Accept protocol lists and auto scan CIDR lists supplied as comma strings.
	switch k {
	case "mapper.protocols", "scanner.auto_scan_cidr", "http.cors_origins":
		return true
	}
	return false
}

// isSecretKey reports whether a settings key holds a credential. Nothing matching
// here may ever be written through the generic settings endpoints; the check is
// deliberately broad because it is a second line of defence behind
// allowedSetting, and names like "bootstrap_pass" do not contain "password".
func isSecretKey(k string) bool {
	l := strings.ToLower(k)
	for _, marker := range []string{"password", "pass", "secret", "token", "credential", "private_key", "apikey", "api_key"} {
		if strings.Contains(l, marker) {
			return true
		}
	}
	return false
}

func (s *Server) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	var raw map[string]any
	if err := decodeJSONLoose(r, &raw); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	kv := make(map[string]string, len(raw))
	for k, v := range raw {
		str, ok := settingValueToString(v)
		if !ok {
			writeError(w, http.StatusBadRequest, "unsupported value type for "+k)
			return
		}
		kv[k] = str
	}
	if err := s.validateSettingOverrides(kv); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	applied, restartRequired, unsupported, cleared, err := s.applySettingOverrides(kv)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.audit(r, "settings.patch", strings.Join(applied, ","), "", true)
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":               true,
		"applied":          applied,
		"restart_required": restartRequired,
		"unsupported":      unsupported,
		"cleared":          cleared,
	})
}

// ---- export / import ----

type exportPayload struct {
	Version   string           `json:"version"`
	Exported  time.Time        `json:"exported_at"`
	Rules     []model.Rule     `json:"rules"`
	Policies  []model.Policy   `json:"policies"`
	Schedules []model.Schedule `json:"schedules"`
	Hosts     []model.Host     `json:"hosts"`
}

func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	format := strings.ToLower(r.URL.Query().Get("format"))
	rules, err := s.app.Store.AllRules()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	policies, _ := s.app.Store.ListPolicies()
	schedules, _ := s.app.Store.ListSchedules()
	hosts, _ := s.app.Store.ListHosts(2000)

	if format == "csv" {
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", "attachment; filename=natpilot-rules.csv")
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "name", "host", "port", "ext_port", "protocol", "tags", "enabled", "source", "note"})
		for _, rule := range rules {
			_ = cw.Write([]string{
				strconv.FormatInt(rule.ID, 10), rule.Name, rule.Host, strconv.Itoa(rule.Port),
				strconv.Itoa(rule.ExtPort), rule.Protocol, strings.Join(rule.Tags, "|"),
				strconv.FormatBool(rule.Enabled), rule.Source, rule.Note,
			})
		}
		cw.Flush()
		return
	}
	writeJSON(w, http.StatusOK, exportPayload{
		Version: s.app.Version, Exported: time.Now().UTC(),
		Rules: rules, Policies: policies, Schedules: schedules, Hosts: hosts,
	})
}

func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	var payload exportPayload
	if err := decodeJSONLoose(r, &payload); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	created, skipped := 0, 0
	for _, rule := range payload.Rules {
		if rule.Port < 1 || rule.Port > 65535 {
			skipped++
			continue
		}
		if existing, _ := s.app.Store.GetRuleByKey(rule.Host, rule.Port, rule.Protocol); existing != nil {
			skipped++
			continue
		}
		nr := rule
		nr.ID = 0
		if _, err := s.app.Store.CreateRule(&nr); err != nil {
			skipped++
			continue
		}
		created++
	}
	for _, p := range payload.Policies {
		p.ID = 0
		if _, err := s.app.Store.CreatePolicy(&p); err == nil {
			created++
		}
	}
	s.audit(r, "import", fmt.Sprintf("created=%d skipped=%d", created, skipped), "", true)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "created": created, "skipped": skipped})
}

var _ = bus.Event{}

// hostAddressFor builds one address record for a host, filling the IPv6 family and
// the link-local scope zone so a dual-stack device can merge its addresses into a
// single host row.
func hostAddressFor(ip, zone, source string) model.HostAddress {
	addr := model.HostAddress{IP: ip, Family: model.IPFamily(ip), Zone: zone, Source: source}
	if !strings.Contains(ip, ":") {
		addr.Zone = ""
	}
	return addr
}

// ---- 自动主机扫描 ----

// autoScanState is the JSON shape of GET/PATCH /api/scans/host/auto.
type autoScanState struct {
	Enabled         bool     `json:"enabled"`
	IntervalSeconds int      `json:"interval_seconds"`
	CIDRs           []string `json:"cidrs"`
	Ports           string   `json:"ports"`
	NextRun         string   `json:"next_run,omitempty"`
}

func (s *Server) autoScanNow() autoScanState {
	cfg := s.app.Config.Scanner
	next := ""
	if cfg.AutoScanEnabled && cfg.AutoIntervalSec > 0 {
		next = time.Now().Add(time.Duration(cfg.AutoIntervalSec) * time.Second).UTC().Format(time.RFC3339)
	}
	return autoScanState{
		Enabled:         cfg.AutoScanEnabled,
		IntervalSeconds: cfg.AutoIntervalSec,
		CIDRs:           append([]string{}, cfg.AutoScanCIDR...),
		Ports:           cfg.DefaultPorts,
		NextRun:         next,
	}
}

func (s *Server) handleGetAutoScan(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auto": s.autoScanNow()})
}

func (s *Server) handlePatchAutoScan(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled         *bool    `json:"enabled"`
		IntervalSeconds *int     `json:"interval_seconds"`
		CIDRs           []string `json:"cidrs"`
		Ports           *string  `json:"ports"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	cfg := &s.app.Config.Scanner
	if req.Enabled != nil {
		cfg.AutoScanEnabled = *req.Enabled
	}
	if req.IntervalSeconds != nil {
		v := *req.IntervalSeconds
		if v < 60 {
			v = 60
		}
		if v > 86400 {
			v = 86400
		}
		cfg.AutoIntervalSec = v
	}
	if req.CIDRs != nil {
		if len(req.CIDRs) > 16 {
			writeError(w, http.StatusBadRequest, "最多 16 个扫描网段")
			return
		}
		cfg.AutoScanCIDR = append([]string{}, req.CIDRs...)
	}
	if req.Ports != nil {
		cfg.DefaultPorts = strings.TrimSpace(*req.Ports)
	}
	// 持久化到 settings（config. 前缀），重启后与其他设置一样被重新应用。
	kv := map[string]string{
		"scanner.auto_scan_enabled":     strconv.FormatBool(cfg.AutoScanEnabled),
		"scanner.auto_interval_seconds": strconv.Itoa(cfg.AutoIntervalSec),
		"scanner.auto_scan_cidr":        strings.Join(cfg.AutoScanCIDR, ","),
	}
	for k, v := range kv {
		_ = s.app.Store.SetSetting("config."+k, v)
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "auto": s.autoScanNow()})
}

// StartAutoScanLoop runs the configured automatic host scans. It re-reads the
// config before every run, so PATCH takes effect without a restart, and it never
// overlaps two runs.
func (s *Server) StartAutoScanLoop(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	var running int32
	last := time.Time{}
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cfg := s.app.Config.Scanner
			if !cfg.AutoScanEnabled || len(cfg.AutoScanCIDR) == 0 {
				continue
			}
			iv := time.Duration(cfg.AutoIntervalSec) * time.Second
			if iv < 60*time.Second {
				iv = 60 * time.Second
			}
			if !last.IsZero() && time.Since(last) < iv {
				continue
			}
			if !atomic.CompareAndSwapInt32(&running, 0, 1) {
				continue
			}
			last = time.Now()
			go func(cidrs []string, ports string) {
				defer atomic.StoreInt32(&running, 0)
				for _, cidr := range cidrs {
					res, err := s.runHostScan(ctx, hostScanParams{Mode: scanModeCIDR, CIDR: cidr, Ports: ports})
					if err != nil {
						logging.Warn("scanner", "auto host scan failed", "cidr", cidr, "error", err.Error())
						continue
					}
					s.app.Bus.PublishType("hosts", map[string]any{"count": res.Discovered, "auto": true})
					logging.Info("scanner", "auto host scan done", "cidr", cidr, "alive", res.Alive, "discovered", res.Discovered)
				}
			}(append([]string{}, cfg.AutoScanCIDR...), cfg.DefaultPorts)
		}
	}
}
