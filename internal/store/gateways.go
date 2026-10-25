package store

import (
	"database/sql"

	"github.com/ekko-studio/natpilot/internal/model"
)

const gwCols = `id, name, kind, address, port, location, control_url, service_type, external_ip, model_name, manufacturer, preferred, enabled, last_seen, last_error, capabilities`

func scanGateway(sc interface{ Scan(...any) error }) (*model.Gateway, error) {
	g := &model.Gateway{}
	var preferred, enabled int
	var lastSeen model.Time
	var caps model.StringList
	err := sc.Scan(&g.ID, &g.Name, &g.Kind, &g.Address, &g.Port, &g.Location, &g.ControlURL, &g.ServiceType,
		&g.ExternalIP, &g.ModelName, &g.Manufacturer, &preferred, &enabled, &lastSeen, &g.LastError, &caps)
	if err != nil {
		return nil, err
	}
	g.Preferred, g.Enabled = i2b(preferred), i2b(enabled)
	g.LastSeen = lastSeen
	g.Capabilities = caps
	if g.Capabilities == nil {
		g.Capabilities = model.StringList{}
	}
	return g, nil
}

// UpsertGateway stores or refreshes a discovered gateway.
func (s *Store) UpsertGateway(g *model.Gateway) error {
	now := model.Time{}
	_ = now.Scan(Now())
	if g.Capabilities == nil {
		g.Capabilities = model.StringList{}
	}
	var id int64
	err := s.db.QueryRow(`SELECT id FROM gateways WHERE address=? AND port=? AND kind=?`, g.Address, g.Port, g.Kind).Scan(&id)
	if err == nil {
		_, err = s.db.Exec(`UPDATE gateways SET name=?, location=?, control_url=?, service_type=?, external_ip=?,
			model_name=?, manufacturer=?, enabled=?, last_seen=?, last_error=?, capabilities=? WHERE id=?`,
			g.Name, g.Location, g.ControlURL, g.ServiceType, g.ExternalIP, g.ModelName, g.Manufacturer,
			b2i(g.Enabled), now, g.LastError, g.Capabilities, id)
		g.ID = id
		g.LastSeen = now
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	res, err := s.db.Exec(`INSERT INTO gateways(name, kind, address, port, location, control_url, service_type, external_ip,
		model_name, manufacturer, preferred, enabled, last_seen, last_error, capabilities)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		g.Name, g.Kind, g.Address, g.Port, g.Location, g.ControlURL, g.ServiceType, g.ExternalIP,
		g.ModelName, g.Manufacturer, b2i(g.Preferred), b2i(g.Enabled), now, g.LastError, g.Capabilities)
	if err != nil {
		return err
	}
	id, _ = res.LastInsertId()
	g.ID = id
	g.LastSeen = now
	return nil
}

// ListGateways returns all known gateways, preferred first.
func (s *Store) ListGateways() ([]model.Gateway, error) {
	rows, err := s.db.Query(`SELECT ` + gwCols + ` FROM gateways ORDER BY preferred DESC, enabled DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Gateway{}
	for rows.Next() {
		g, err := scanGateway(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *g)
	}
	return out, rows.Err()
}

// GetGateway fetches a gateway by id.
func (s *Store) GetGateway(id int64) (*model.Gateway, error) {
	row := s.db.QueryRow(`SELECT `+gwCols+` FROM gateways WHERE id=?`, id)
	g, err := scanGateway(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

// DeleteGateway removes a gateway entry.
func (s *Store) DeleteGateway(id int64) error {
	_, err := s.db.Exec(`DELETE FROM gateways WHERE id=?`, id)
	return err
}

// SetGatewayPreferred makes one gateway preferred and clears the others.
func (s *Store) SetGatewayPreferred(id int64) error {
	if _, err := s.db.Exec(`UPDATE gateways SET preferred=0`); err != nil {
		return err
	}
	_, err := s.db.Exec(`UPDATE gateways SET preferred=1 WHERE id=?`, id)
	return err
}

// SetGatewayEnabled toggles a gateway's participation.
func (s *Store) SetGatewayEnabled(id int64, enabled bool) error {
	_, err := s.db.Exec(`UPDATE gateways SET enabled=? WHERE id=?`, b2i(enabled), id)
	return err
}

// PreferredGateway returns the first enabled+preferred gateway, if any.
func (s *Store) PreferredGateway() (*model.Gateway, error) {
	row := s.db.QueryRow(`SELECT ` + gwCols + ` FROM gateways WHERE enabled=1 ORDER BY preferred DESC, id ASC LIMIT 1`)
	g, err := scanGateway(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return g, err
}

// ClearGateways removes every gateway row (used before a full re-discovery).
func (s *Store) ClearGateways() error {
	_, err := s.db.Exec(`DELETE FROM gateways`)
	return err
}

// ---- hosts ----

const hostCols = `id, ip, mac, hostname, vendor, kind, iface, is_local, seen_count, open_ports, tags, note, first_seen, last_seen, source`

func scanHost(sc interface{ Scan(...any) error }) (*model.Host, error) {
	h := &model.Host{}
	var isLocal int
	var firstSeen, lastSeen model.Time
	var openPorts, tags model.StringList
	err := sc.Scan(&h.ID, &h.IP, &h.MAC, &h.Hostname, &h.Vendor, &h.Kind, &h.Interface, &isLocal, &h.SeenCount,
		&openPorts, &tags, &h.Note, &firstSeen, &lastSeen, &h.Source)
	if err != nil {
		return nil, err
	}
	h.IsLocal = i2b(isLocal)
	h.FirstSeen, h.LastSeen = firstSeen, lastSeen
	h.OpenPorts, h.Tags = openPorts, tags
	if h.OpenPorts == nil {
		h.OpenPorts = model.StringList{}
	}
	if h.Tags == nil {
		h.Tags = model.StringList{}
	}
	return h, nil
}

// UpsertHost records a host observation.
//
// Identity is the MAC address when the observation carries one: a DHCP lease
// change means the same device shows up at a new IP, and matching on IP alone
// would create a second row (losing the "this is the host I monitor" link) and
// leave a stale entry behind. The lookup falls back to the IP for observations
// without a MAC (loopback interfaces, some virtual adapters).
func (s *Store) UpsertHost(h *model.Host) error {
	now := model.Time{}
	_ = now.Scan(Now())
	mac := normalizeMAC(h.MAC)

	var id int64
	var seen int
	var err error
	if mac != "" {
		err = s.db.QueryRow(`SELECT id, seen_count FROM hosts WHERE mac=?`, mac).Scan(&id, &seen)
		if err == nil {
			// Same hardware, possibly a new address: keep the row and its id.
			if h.OpenPorts == nil {
				h.OpenPorts = model.StringList{}
			}
			if h.Tags == nil {
				h.Tags = model.StringList{}
			}
			_, err = s.db.Exec(`UPDATE hosts SET ip=?, mac=?, hostname=?, vendor=?, kind=?, iface=?, is_local=?, seen_count=?,
				open_ports=?, tags=?, note=?, last_seen=?, source=? WHERE id=?`,
				h.IP, mac, h.Hostname, h.Vendor, h.Kind, h.Interface, b2i(h.IsLocal), seen+1, h.OpenPorts, h.Tags, h.Note, now, h.Source, id)
			h.ID = id
			h.MAC = mac
			h.SeenCount = seen + 1
			h.LastSeen = now
			return err
		}
		if err != sql.ErrNoRows {
			return err
		}
	}

	err = s.db.QueryRow(`SELECT id, seen_count FROM hosts WHERE ip=?`, h.IP).Scan(&id, &seen)
	if err == nil {
		if h.OpenPorts == nil {
			h.OpenPorts = model.StringList{}
		}
		if h.Tags == nil {
			h.Tags = model.StringList{}
		}
		_, err = s.db.Exec(`UPDATE hosts SET mac=?, hostname=?, vendor=?, kind=?, iface=?, is_local=?, seen_count=?,
			open_ports=?, tags=?, note=?, last_seen=?, source=? WHERE id=?`,
			mac, h.Hostname, h.Vendor, h.Kind, h.Interface, b2i(h.IsLocal), seen+1, h.OpenPorts, h.Tags, h.Note, now, h.Source, id)
		h.ID = id
		h.MAC = mac
		h.SeenCount = seen + 1
		h.LastSeen = now
		return err
	}
	if err != sql.ErrNoRows {
		return err
	}
	if h.OpenPorts == nil {
		h.OpenPorts = model.StringList{}
	}
	if h.Tags == nil {
		h.Tags = model.StringList{}
	}
	res, err := s.db.Exec(`INSERT INTO hosts(ip, mac, hostname, vendor, kind, iface, is_local, seen_count, open_ports, tags, note, first_seen, last_seen, source)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		h.IP, mac, h.Hostname, h.Vendor, h.Kind, h.Interface, b2i(h.IsLocal), 1, h.OpenPorts, h.Tags, h.Note, now, now, h.Source)
	if err != nil {
		return err
	}
	id, _ = res.LastInsertId()
	h.ID = id
	h.MAC = mac
	h.SeenCount = 1
	h.FirstSeen, h.LastSeen = now, now
	return nil
}

// ListHosts returns known hosts.
func (s *Store) ListHosts(limit int) ([]model.Host, error) {
	if limit <= 0 {
		limit = 500
	}
	rows, err := s.db.Query(`SELECT `+hostCols+` FROM hosts ORDER BY last_seen DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []model.Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *h)
	}
	return out, rows.Err()
}

// GetHostByIP finds a host by address.
func (s *Store) GetHostByIP(ip string) (*model.Host, error) {
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE ip=?`, ip)
	h, err := scanHost(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return h, err
}

// GetHostByMAC finds a host by hardware address. This is the stable identity a
// host-bound policy resolves against, because DHCP may move the IP at any time.
func (s *Store) GetHostByMAC(mac string) (*model.Host, error) {
	mac = normalizeMAC(mac)
	if mac == "" {
		return nil, nil
	}
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE mac=?`, mac)
	h, err := scanHost(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return h, err
}

// GetHostByID finds a host by primary key.
func (s *Store) GetHostByID(id int64) (*model.Host, error) {
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE id=?`, id)
	h, err := scanHost(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return h, err
}

// DeleteHost removes a host observation.
func (s *Store) DeleteHost(id int64) error {
	_, err := s.db.Exec(`DELETE FROM hosts WHERE id=?`, id)
	return err
}

// CountHosts returns the number of known hosts.
func (s *Store) CountHosts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM hosts`).Scan(&n)
	return n, err
}
