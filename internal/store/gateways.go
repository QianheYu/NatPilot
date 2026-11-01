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
// leave a stale entry behind. When the MAC is unknown the lookup falls back to
// any address already recorded for the host (host_addresses first, then the
// legacy hosts.ip column), so observations without a MAC (loopback interfaces,
// some virtual adapters) still merge.
//
// Every address carried by the observation (h.Addresses plus the legacy h.IP)
// is upserted into host_addresses; hosts.ip is then recomputed as the primary
// address using the rule documented on pickPrimary. A device discovered over
// both ARP and NDP therefore keeps both an IPv4 and an IPv6 address instead of
// losing whichever family was written last.
func (s *Store) UpsertHost(h *model.Host) error {
	if h == nil {
		return nil
	}
	now := model.Time{}
	_ = now.Scan(Now())
	mac := normalizeMAC(h.MAC)
	cands := hostCandidates(h)
	if h.OpenPorts == nil {
		h.OpenPorts = model.StringList{}
	}
	if h.Tags == nil {
		h.Tags = model.StringList{}
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	id, seen, firstSeen, err := findHostRow(tx, mac, cands)
	found := err == nil
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	// A new row may only be anchored at an address nobody else owns: hosts.ip is
	// unique and an address already stored for another device stays there.
	newIP := ""
	if !found {
		newIP, err = firstUnclaimedAddress(tx, cands)
		if err != nil {
			return err
		}
		if newIP == "" && len(cands) > 0 {
			// Every observed address belongs to another row: fall back to the
			// legacy behaviour of refreshing the owning row instead of failing.
			id, seen, firstSeen, err = findHostRowByAddress(tx, cands)
			switch {
			case err == nil:
				found = true
			case err != sql.ErrNoRows:
				return err
			}
		}
	}

	if found {
		// Same hardware, possibly a new address: keep the row and its id. An
		// observation without a MAC must not erase the MAC learned earlier.
		_, err = tx.Exec(`UPDATE hosts SET mac=CASE WHEN ? <> '' THEN ? ELSE mac END,
			hostname=?, vendor=?, kind=?, iface=?, is_local=?, seen_count=?,
			open_ports=?, tags=?, note=?, last_seen=?, source=? WHERE id=?`,
			mac, mac, h.Hostname, h.Vendor, h.Kind, h.Interface, b2i(h.IsLocal), seen+1,
			h.OpenPorts, h.Tags, h.Note, now, h.Source, id)
		if err != nil {
			return err
		}
	} else {
		res, ierr := tx.Exec(`INSERT INTO hosts(ip, mac, hostname, vendor, kind, iface, is_local, seen_count, open_ports, tags, note, first_seen, last_seen, source)
			VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			newIP, mac, h.Hostname, h.Vendor, h.Kind, h.Interface, b2i(h.IsLocal), 1,
			h.OpenPorts, h.Tags, h.Note, now, now, h.Source)
		if ierr != nil {
			return ierr
		}
		id, _ = res.LastInsertId()
		seen = 0
		firstSeen = now
	}
	h.ID = id
	h.MAC = mac
	h.SeenCount = seen + 1
	h.FirstSeen = firstSeen
	h.LastSeen = now

	if err := mergeHostAddresses(tx, id, cands, now); err != nil {
		return err
	}
	addrs, err := loadHostAddresses(tx, id)
	if err != nil {
		return err
	}
	if best, ok := pickPrimary(addrs); ok {
		if err := setPrimaryIP(tx, id, best.IP); err != nil {
			return err
		}
	}
	// Report what is actually persisted: setPrimaryIP may have kept the previous
	// value when another host row still owns the preferred address, and the MAC
	// may have been preserved from an earlier observation.
	var storedMAC, primaryIP string
	if err := tx.QueryRow(`SELECT mac, ip FROM hosts WHERE id=?`, id).Scan(&storedMAC, &primaryIP); err != nil {
		return err
	}
	h.MAC = storedMAC
	h.IP = primaryIP
	h.Addresses = addrs
	annotatePrimaryAddress(h)
	return tx.Commit()
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
	hosts := []*model.Host{}
	for rows.Next() {
		h, err := scanHost(rows)
		if err != nil {
			return nil, err
		}
		hosts = append(hosts, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Close before the address query: the store runs on a single connection.
	rows.Close()
	if err := s.attachHostAddresses(hosts); err != nil {
		return nil, err
	}
	out := make([]model.Host, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, *h)
	}
	return out, nil
}

// GetHostByIP finds a host by address. The address may be the primary one
// (hosts.ip) or any secondary address recorded for the device, so callers that
// scan an IPv6 address still find the host whose primary address is IPv4.
func (s *Store) GetHostByIP(ip string) (*model.Host, error) {
	canonical, _ := canonicalAddress(ip)
	if canonical == "" {
		return nil, nil
	}
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts h WHERE EXISTS (
		SELECT 1 FROM host_addresses a WHERE a.host_id = h.id AND a.ip = ?) LIMIT 1`, canonical)
	h, err := scanHost(row)
	if err == sql.ErrNoRows {
		// Legacy/manual rows written before host_addresses existed.
		row = s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE ip=?`, canonical)
		h, err = scanHost(row)
	}
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.attachHostAddresses([]*model.Host{h}); err != nil {
		return nil, err
	}
	return h, nil
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
	if err != nil {
		return nil, err
	}
	if err := s.attachHostAddresses([]*model.Host{h}); err != nil {
		return nil, err
	}
	return h, nil
}

// GetHostByID finds a host by primary key.
func (s *Store) GetHostByID(id int64) (*model.Host, error) {
	row := s.db.QueryRow(`SELECT `+hostCols+` FROM hosts WHERE id=?`, id)
	h, err := scanHost(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if err := s.attachHostAddresses([]*model.Host{h}); err != nil {
		return nil, err
	}
	return h, nil
}

// DeleteHost removes a host observation and every address recorded for it.
// The addresses are deleted explicitly because SQLite does not enforce the
// foreign key (ON DELETE CASCADE) unless foreign_keys is enabled per connection.
func (s *Store) DeleteHost(id int64) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(`DELETE FROM host_addresses WHERE host_id=?`, id); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM hosts WHERE id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

// CountHosts returns the number of known hosts.
func (s *Store) CountHosts() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM hosts`).Scan(&n)
	return n, err
}
