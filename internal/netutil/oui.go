package netutil

import "strings"

// ouiVendors is a deliberately small built-in vendor table for the most common
// home router / consumer network chipset prefixes. A full IEEE OUI database can
// be dropped in as a text file; see LoadOUIFile.
var ouiVendors = map[string]string{
	"00:1a:2b": "Ayecom",
	"00:1c:df": "Belkin",
	"00:1e:58": "D-Link",
	"00:1f:33": "Netgear",
	"00:24:b2": "Netgear",
	"00:26:5a": "D-Link",
	"00:0c:29": "VMware",
	"00:05:69": "VMware",
	"00:50:56": "VMware",
	"08:00:27": "VirtualBox",
	"52:54:00": "QEMU",
	"00:16:3e": "Xen",
	"02:42:ac": "Docker",
	"b8:27:eb": "Raspberry Pi",
	"dc:a6:32": "Raspberry Pi",
	"e4:5f:01": "Raspberry Pi",
	"00:1d:7e": "Cisco-Linksys",
	"00:23:69": "Cisco-Linksys",
	"c0:56:27": "Belkin",
	"00:0c:43": "MediaTek",
	"00:1e:64": "Intel",
	"00:1b:21": "Intel",
	"3c:97:0e": "Wistron",
	"f4:f2:6d": "TP-Link",
	"50:c7:bf": "TP-Link",
	"60:32:b1": "TP-Link",
	"a4:2b:b0": "TP-Link",
	"b0:4e:26": "TP-Link",
	"c4:6e:1f": "TP-Link",
	"ec:08:6b": "TP-Link",
	"00:1e:2a": "Netgear",
	"20:4e:7f": "Netgear",
	"2c:30:33": "Netgear",
	"44:94:fc": "Netgear",
	"9c:3d:cf": "Netgear",
	"a0:40:a0": "Netgear",
	"00:11:32": "Synology",
	"00:1e:06": "Wibrain",
	"18:fe:34": "Espressif",
	"24:0a:c4": "Espressif",
	"3c:71:bf": "Espressif",
	"84:cc:a8": "Espressif",
	"a4:cf:12": "Espressif",
	"00:1a:79": "Huawei",
	"00:e0:fc": "Huawei",
	"28:6e:d4": "Huawei",
	"48:43:7c": "Huawei",
	"70:72:3c": "Huawei",
	"00:15:5d": "Microsoft Hyper-V",
	"00:03:ff": "Microsoft",
	"00:25:9c": "Cisco-Linksys",
	"68:7f:74": "Cisco-Linksys",
	"00:26:b9": "Dell",
	"b8:2a:72": "Dell",
	"00:1c:c4": "HP",
	"00:26:55": "HP",
	"00:21:5a": "HP",
	"d8:d3:85": "HP",
	"00:11:22": "Cimsys",
	"00:25:22": "ASRock",
	"bc:ae:c5": "ASUS",
	"04:d4:c4": "ASUS",
	"2c:56:dc": "ASUS",
	"50:46:5d": "ASUS",
	"ac:9e:17": "ASUS",
	"00:0f:b5": "Netgear",
	"00:13:10": "Cisco-Linksys",
	"00:18:f8": "Cisco-Linksys",
	"00:22:6b": "Cisco-Linksys",
}

// LoadOUIFile merges an external OUI file (lines of "AABBCC Vendor" or
// "AA:BB:CC Vendor") into the lookup table.
func LoadOUIFile(path string, readFile func(string) ([]byte, error)) error {
	b, err := readFile(path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.SplitN(line, " ", 2)
		if len(fields) == 0 {
			continue
		}
		prefix := strings.ToLower(strings.TrimSpace(fields[0]))
		prefix = strings.ReplaceAll(prefix, "-", ":")
		if len(prefix) == 6 {
			prefix = prefix[0:2] + ":" + prefix[2:4] + ":" + prefix[4:6]
		}
		if len(prefix) != 8 {
			continue
		}
		vendor := ""
		if len(fields) > 1 {
			vendor = strings.TrimSpace(fields[1])
		}
		ouiVendors[prefix] = vendor
	}
	return nil
}

// MACVendor returns a best-effort vendor for a MAC address.
func MACVendor(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	if len(mac) < 8 {
		return ""
	}
	if v, ok := ouiVendors[mac[:8]]; ok {
		return v
	}
	// Some NICs randomize the U/L bit; retry with it cleared for locally
	// administered addresses.
	if len(mac) >= 2 {
		if b, err := hexByte(mac[0:2]); err == nil && b&0x02 != 0 {
			local := mac[0:8]
			cleared := byteToHex(b &^ 0x02)
			local = cleared + mac[2:8]
			if v, ok := ouiVendors[local]; ok {
				return v
			}
		}
	}
	return ""
}

func hexByte(s string) (byte, error) {
	var out byte
	for i := 0; i < 2; i++ {
		out <<= 4
		switch c := s[i]; {
		case c >= '0' && c <= '9':
			out |= c - '0'
		case c >= 'a' && c <= 'f':
			out |= c - 'a' + 10
		case c >= 'A' && c <= 'F':
			out |= c - 'A' + 10
		default:
			return 0, errBadHex
		}
	}
	return out, nil
}

func byteToHex(b byte) string {
	const h = "0123456789abcdef"
	return string([]byte{h[b>>4], h[b&0xf]})
}

var errBadHex = &hexError{}

type hexError struct{}

func (*hexError) Error() string { return "invalid hex" }

// NormalizeMAC lowercases and canonicalises a MAC address.
func NormalizeMAC(mac string) string {
	mac = strings.ToLower(strings.TrimSpace(mac))
	mac = strings.ReplaceAll(mac, "-", ":")
	return mac
}

// ErrBadHex is exported for callers that validate addresses.
var ErrBadHex = errBadHex
