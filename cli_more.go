package main

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/ekko-studio/natpilot/internal/app"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/proto"
	"github.com/ekko-studio/natpilot/internal/scanner"
	"github.com/ekko-studio/natpilot/internal/scheduler"
	"github.com/ekko-studio/natpilot/internal/store"
)

func leadingID(args []string) (int64, []string, bool) {
	rest := []string{}
	var id int64
	found := false
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			continue
		}
		if !found {
			n, err := strconv.ParseInt(a, 10, 64)
			if err != nil {
				return 0, args, false
			}
			id = n
			found = true
			continue
		}
		rest = append(rest, a)
	}
	return id, rest, found
}

func leadingIDs(args []string) []int64 {
	out := []int64{}
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			continue
		}
		if n, err := strconv.ParseInt(a, 10, 64); err == nil {
			out = append(out, n)
		}
	}
	return out
}

func scanRequestFromKV(kind string, rest []string, kv map[string]string, portsSpec string) scanner.Request {
	target := model.ScanTargetLocal
	ref := ""
	switch kind {
	case "local", "localhost", "self":
		target = model.ScanTargetLocal
	case "cidr":
		target = model.ScanTargetCIDR
		if len(rest) > 0 {
			ref = rest[0]
		}
		if ref == "" {
			ref = kv["cidr"]
		}
	case "remote", "lan":
		target = model.ScanTargetRemote
		if len(rest) > 0 {
			ref = rest[0]
		}
		if ref == "" {
			ref = kv["cidr"]
		}
	default:
		// A bare CIDR argument implies a network scan.
		if strings.Contains(kind, "/") {
			target = model.ScanTargetCIDR
			ref = kind
		} else {
			target = model.ScanTargetLocal
		}
	}
	return scanner.Request{
		Name:        kv["name"],
		TargetKind:  target,
		TargetRef:   ref,
		PortsSpec:   portsSpec,
		FilterPorts: strings.TrimSpace(kv["ports"]) != "",
		Protocol:    kv["protocol"],
		Trigger:     "manual",
		ApplyPolicy: flagBool(kv, "apply-policy", true),
		MaxHosts:    flagInt(kv, "max-hosts", 0),
	}
}

// promoteOpenPorts turns newly discovered open ports into managed rules.
func promoteOpenPorts(ctx context.Context, a *app.App, scanID int64, kv map[string]string) (int, int) {
	ports, _, err := a.Store.ListPorts(store.PortStatusFilter{ScanID: scanID, Limit: 5000})
	if err != nil {
		return 0, 0
	}
	tags := model.StringList(flagList(kv, "tags"))
	added, applied := 0, 0
	for _, p := range ports {
		if p.Added || p.Ignored {
			continue
		}
		if p.Status != model.StatusOpen && p.Status != model.StatusNotAdded {
			continue
		}
		if existing, _ := a.Store.GetRuleByKey(p.Host, p.Port, p.Protocol); existing != nil {
			continue
		}
		rule := &model.Rule{
			Name:     fmt.Sprintf("%s %s:%d", strings.ToUpper(p.Protocol), p.Host, p.Port),
			Host:     p.Host,
			Port:     p.Port,
			Protocol: p.Protocol,
			Tags:     tags,
			Note:     "added by scan --add-open",
			Enabled:  true,
			Source:   "scan",
		}
		if rule.Tags == nil {
			rule.Tags = model.StringList{}
		}
		rid, err := a.Store.CreateRule(rule)
		if err != nil {
			continue
		}
		rule.ID = rid
		_ = a.Store.MarkPortAdded(p.ID, rid)
		added++
		if _, err := a.Mapper.ApplyRule(ctx, *rule); err == nil {
			applied++
		}
	}
	return added, applied
}

// ---- gateway ----

func cmdGateway(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		gws := a.Mapper.GatewayList()
		if asJSON {
			printJSON(map[string]any{"gateways": gws, "preferred": a.Mapper.PreferredKey()})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tKIND\tNAME\tADDRESS\tEXTERNAL IP\tCAPABILITIES\tPREF")
		for _, g := range gws {
			pref := ""
			if g.Preferred {
				pref = "yes"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
				g.ID, g.Kind, truncateCell(g.Name, 24), fmt.Sprintf("%s:%d", g.Address, g.Port),
				orDash(g.ExternalIP), strings.Join(g.Capabilities, ","), pref)
		}
		tw.Flush()
		if len(gws) == 0 {
			fmt.Println("no gateways known yet; run: natpilot gateway discover")
		}
		return 0
	}
	switch args[0] {
	case "discover", "scan":
		gws, err := a.Mapper.Discover(ctx)
		if err != nil {
			return fatal("%v", err)
		}
		var ip string
		if a.Config.Mapper.ExternalIPCheck {
			ip, _ = a.Mapper.ExternalIP(ctx)
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "gateways": gws, "external_ip": ip})
			return 0
		}
		fmt.Printf("discovered %d gateway(s)\n", len(gws))
		for _, g := range gws {
			fmt.Printf("  %-6s %-30s %s:%d  caps=%s\n", g.Kind, truncateCell(g.Name, 30), g.Address, g.Port, strings.Join(g.Capabilities, ","))
		}
		if ip != "" {
			fmt.Printf("external IP: %s\n", ip)
		}
		return 0
	case "preferred":
		kv, rest := parseKV(args[1:])
		key := kv["key"]
		if key == "" && len(rest) > 0 {
			// Accept an "id" or "kind:addr" form.
			if id, err := strconv.ParseInt(rest[0], 10, 64); err == nil {
				if gw, _ := a.Store.GetGateway(id); gw != nil {
					key = fmt.Sprintf("%s|%s|%d", gw.Kind, gw.Address, gw.Port)
				}
			}
		}
		if key == "" {
			return fatal("usage: gateway preferred <id> | --key kind|addr|port")
		}
		a.Mapper.SetPreferred(key)
		if asJSON {
			printJSON(map[string]any{"ok": true, "preferred": key})
			return 0
		}
		fmt.Printf("preferred gateway: %s\n", key)
		return 0
	case "mappings":
		list, err := a.Mapper.ListGatewayMappings(ctx)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"mappings": list})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "PROTO\tEXTERNAL\tINTERNAL\tLEASE\tDESCRIPTION")
		for _, m := range list {
			fmt.Fprintf(tw, "%s\t:%d\t%s:%d\t%d\t%s\n", m.Protocol, m.ExternalPort, m.InternalHost, m.InternalPort, m.LeaseSeconds, m.Description)
		}
		tw.Flush()
		return 0
	default:
		return fatal("unknown gateway subcommand %q (list|discover|preferred|mappings)", args[0])
	}
}

// ---- mapping ----

func cmdMapping(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	sub := "list"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "list", "ls":
		records := a.Mapper.Records()
		ids := make([]int64, 0, len(records))
		for id := range records {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		if asJSON {
			printJSON(map[string]any{"records": records})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "RULE\tSTATUS\tPROVIDER\tEXTERNAL\tINTERNAL\tLEASE\tERROR")
		for _, id := range ids {
			rec := records[id]
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s:%d\t%s:%d\t%ds\t%s\n",
				id, rec.Status, truncateCell(rec.ProviderName, 22), orDash(rec.ExternalIP), rec.ExternalPort,
				rec.InternalHost, rec.InternalPort, rec.LeaseSeconds, truncateCell(rec.Error, 30))
		}
		tw.Flush()
		if len(ids) == 0 {
			fmt.Println("no live mapping records; run: natpilot mapping reconcile")
		}
		return 0
	case "reconcile", "sync":
		sum, err := a.Mapper.Reconcile(ctx)
		if asJSON {
			out := map[string]any{"ok": err == nil, "summary": sum}
			if err != nil {
				out["error"] = err.Error()
			}
			printJSON(out)
			return 0
		}
		fmt.Println("reconcile:", sum.String())
		if err != nil {
			return fatal("%v", err)
		}
		return 0
	case "cleanup":
		rules, err := a.Store.AllRules()
		if err != nil {
			return fatal("%v", err)
		}
		removed := 0
		for _, rule := range rules {
			if rule.Enabled {
				continue
			}
			if _, ok := a.Mapper.Records()[rule.ID]; ok {
				if err := a.Mapper.RemoveMapping(ctx, rule); err == nil {
					removed++
				}
			}
		}
		_ = a.Store.ReconcilePortStatuses()
		if asJSON {
			printJSON(map[string]any{"ok": true, "removed": removed})
			return 0
		}
		fmt.Printf("removed %d stale mapping(s)\n", removed)
		return 0
	case "external-ip", "external":
		ip, err := a.Mapper.ExternalIP(ctx)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"external_ip": ip})
			return 0
		}
		fmt.Println(ip)
		return 0
	default:
		return fatal("unknown mapping subcommand %q (list|reconcile|cleanup|external-ip)", sub)
	}
}

// ---- policy ----

func cmdPolicy(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		pols, err := a.Store.ListPolicies()
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"policies": pols})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tON\tNAME\tTARGET\tPORTS\tACTION\tAUTO-ADD\tPRIORITY")
		for _, p := range pols {
			on := "no"
			if p.Enabled {
				on = "yes"
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%v\t%d\n",
				p.ID, on, truncateCell(p.Name, 26), p.MatchTarget, truncateCell(p.PortRanges, 24), p.AutoAction, p.AutoAdd, p.Priority)
		}
		tw.Flush()
		return 0
	}
	switch args[0] {
	case "add", "create":
		kv, rest := parseKV(args[1:])
		name := kv["name"]
		if name == "" && len(rest) > 0 {
			name = rest[0]
		}
		if name == "" {
			return fatal("usage: policy add --name NAME --ports 3000-3010 --action watch|add|ignore [--target any] [--enable]")
		}
		p := &model.Policy{
			Name:          name,
			Description:   kv["description"],
			Enabled:       flagBool(kv, "enable", false),
			MatchTarget:   orDefault(kv["target"], "any"),
			PortRanges:    kv["ports"],
			ExcludeRanges: kv["exclude"],
			Protocol:      orDefault(kv["protocol"], "any"),
			AutoAction:    orDefault(kv["action"], model.ActionWatch),
			AutoAdd:       flagBool(kv, "auto-add", false),
			Tags:          model.StringList(flagList(kv, "tags")),
			Note:          kv["note"],
			MinPort:       flagInt(kv, "min-port", 0),
			MaxPort:       flagInt(kv, "max-port", 0),
			Priority:      flagInt(kv, "priority", 0),
		}
		id, err := a.Store.CreatePolicy(p)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "policy": p})
			return 0
		}
		fmt.Printf("created policy #%d %q (enabled=%v)\n", id, p.Name, p.Enabled)
		return 0
	case "rm", "delete":
		ids := leadingIDs(args[1:])
		if len(ids) == 0 {
			return fatal("usage: policy rm <id>")
		}
		for _, id := range ids {
			_ = a.Store.DeletePolicy(id)
		}
		fmt.Printf("deleted %d policy(ies)\n", len(ids))
		return 0
	case "enable", "disable":
		ids := leadingIDs(args[1:])
		enabled := args[0] == "enable"
		n := 0
		for _, id := range ids {
			p, err := a.Store.GetPolicy(id)
			if err != nil || p == nil {
				continue
			}
			p.Enabled = enabled
			if err := a.Store.UpdatePolicy(p); err == nil {
				n++
			}
		}
		fmt.Printf("%s: %d policy(ies)\n", args[0], n)
		return 0
	default:
		return fatal("unknown policy subcommand %q (list|add|rm|enable|disable)", args[0])
	}
}

// ---- schedule ----

func cmdSchedule(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		list, err := a.Store.ListSchedules()
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"schedules": list})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tON\tKIND\tCRON\tTARGET\tLAST\tNEXT")
		for _, sc := range list {
			on := "no"
			if sc.Enabled {
				on = "yes"
			}
			next := "-"
			if n, err := scheduler.NextAfter(sc.Cron, time.Now()); err == nil {
				next = n.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s %s\t%s\t%s\n",
				sc.ID, on, sc.Kind, sc.Cron, sc.TargetKind, sc.TargetRef, sc.LastStatus, next)
		}
		tw.Flush()
		return 0
	}
	switch args[0] {
	case "add", "create":
		kv, rest := parseKV(args[1:])
		name := kv["name"]
		if name == "" && len(rest) > 0 {
			name = rest[0]
		}
		cron := kv["cron"]
		if cron == "" {
			return fatal("usage: schedule add --cron \"*/30 * * * *\" --kind scan|reconcile|cleanup|discover [--ports 1-1024]")
		}
		if err := scheduler.Validate(cron); err != nil {
			return fatal("%v", err)
		}
		kind := orDefault(kv["kind"], "scan")
		sc := &model.Schedule{
			Name:       orDefault(name, kind+" job"),
			Kind:       kind,
			Enabled:    flagBool(kv, "enable", true),
			Cron:       cron,
			TargetKind: orDefault(kv["target"], model.ScanTargetLocal),
			TargetRef:  kv["cidr"],
			PortsSpec:  kv["ports"],
			PolicyID:   int64(flagInt(kv, "policy", 0)),
		}
		id, err := a.Store.CreateSchedule(sc)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "schedule": sc})
			return 0
		}
		fmt.Printf("created schedule #%d (%s, %s)\n", id, sc.Kind, sc.Cron)
		return 0
	case "rm", "delete":
		ids := leadingIDs(args[1:])
		for _, id := range ids {
			_ = a.Store.DeleteSchedule(id)
		}
		fmt.Printf("deleted %d schedule(s)\n", len(ids))
		return 0
	default:
		return fatal("unknown schedule subcommand %q (list|add|rm)", args[0])
	}
}

// ---- user ----

func cmdUser(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 || args[0] == "list" || args[0] == "ls" {
		users, err := a.Store.ListUsers()
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"users": users})
			return 0
		}
		tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, "ID\tUSERNAME\tNAME\tROLE\tON\tLAST LOGIN")
		for _, u := range users {
			on := "yes"
			if !u.Enabled {
				on = "no"
			}
			last := "-"
			if u.LastLoginAt.Valid {
				last = u.LastLoginAt.Time.Time.Format("2006-01-02 15:04")
			}
			fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\n", u.ID, u.Username, u.DisplayName, u.Role, on, last)
		}
		tw.Flush()
		return 0
	}
	switch args[0] {
	case "add", "create":
		kv, rest := parseKV(args[1:])
		username := kv["username"]
		if username == "" && len(rest) > 0 {
			username = rest[0]
		}
		if username == "" {
			return fatal("usage: user add --username NAME [--password PW] [--role admin|operator|viewer]")
		}
		password := kv["password"]
		generated := false
		if password == "" {
			password = randomPasswordCLI(16)
			generated = true
		}
		hash, err := hashPasswordCLI(password)
		if err != nil {
			return fatal("%v", err)
		}
		u := &model.User{
			Username:     username,
			DisplayName:  orDefault(kv["name"], username),
			Email:        kv["email"],
			Role:         orDefault(kv["role"], "operator"),
			Enabled:      true,
			PasswordHash: hash,
		}
		id, err := a.Store.CreateUser(u)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			out := map[string]any{"ok": true, "user_id": id, "username": username}
			if generated {
				out["generated_password"] = password
			}
			printJSON(out)
			return 0
		}
		fmt.Printf("created user #%d %s (role=%s)\n", id, username, u.Role)
		if generated {
			fmt.Printf("generated password: %s\n", password)
		}
		return 0
	case "rm", "delete":
		ids := leadingIDs(args[1:])
		for _, id := range ids {
			_ = a.Store.DeleteUser(id)
		}
		fmt.Printf("deleted %d user(s)\n", len(ids))
		return 0
	case "passwd", "password":
		kv, rest := parseKV(args[1:])
		username := kv["username"]
		if username == "" && len(rest) > 0 {
			username = rest[0]
		}
		if username == "" {
			return fatal("usage: user passwd <username> --password NEW")
		}
		u, err := a.Store.GetUserByName(username)
		if err != nil || u == nil {
			return fatal("user %q not found", username)
		}
		pw := kv["password"]
		generated := false
		if pw == "" {
			pw = randomPasswordCLI(16)
			generated = true
		}
		hash, err := hashPasswordCLI(pw)
		if err != nil {
			return fatal("%v", err)
		}
		u.PasswordHash = hash
		if err := a.Store.UpdateUser(u); err != nil {
			return fatal("%v", err)
		}
		_ = a.Store.DeleteUserSessions(u.ID)
		fmt.Printf("password updated for %s\n", username)
		if generated {
			fmt.Printf("new password: %s\n", pw)
		}
		return 0
	case "token":
		ids := leadingIDs(args[1:])
		if len(ids) == 0 {
			return fatal("usage: user token <id>")
		}
		u, err := a.Store.GetUser(ids[0])
		if err != nil || u == nil {
			return fatal("user #%d not found", ids[0])
		}
		token, err := a.Auth.SignToken(u)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(map[string]any{"ok": true, "token": token, "username": u.Username})
			return 0
		}
		fmt.Println(token)
		return 0
	default:
		return fatal("unknown user subcommand %q (list|add|rm|passwd|token)", args[0])
	}
}

// ---- events ----

func cmdEvents(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) > 0 && (args[0] == "clear") {
		keep := 0
		if len(args) > 1 {
			if n, err := strconv.Atoi(args[1]); err == nil {
				keep = n
			}
		}
		n, err := a.Store.ClearEvents(keep)
		if err != nil {
			return fatal("%v", err)
		}
		fmt.Printf("cleared %d event(s)\n", n)
		return 0
	}
	kv, _ := parseKV(args)
	f := store.EventFilter{
		Level:    kv["level"],
		Category: kv["category"],
		Query:    kv["q"],
		Limit:    flagInt(kv, "limit", 50),
	}
	if _, ok := kv["unread"]; ok {
		f.Unread = flagBool(kv, "unread", true)
	}
	events, total, err := a.Store.ListEvents(f)
	if err != nil {
		return fatal("%v", err)
	}
	if asJSON {
		printJSON(map[string]any{"events": events, "total": total})
		return 0
	}
	for _, e := range events {
		fmt.Printf("%s %-5s %-10s %s\n",
			e.At.Time.Format("2006-01-02 15:04:05"), strings.ToUpper(e.Level), e.Category, e.Message)
	}
	fmt.Printf("\n%d event(s)\n", total)
	return 0
}

// ---- config ----

func cmdConfig(flags map[string]string, args []string, asJSON bool) int {
	sub := "print"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "print", "show":
		cfg, err := configLoadForCLI(flags)
		if err != nil {
			return fatal("%v", err)
		}
		if asJSON {
			printJSON(cfg.RedactedMap())
			return 0
		}
		b, err := marshalYAML(cfg)
		if err != nil {
			return fatal("%v", err)
		}
		fmt.Print(string(b))
		return 0
	case "path":
		cfg, _ := configLoadForCLI(flags)
		fmt.Println(cfg.App.DataDir)
		fmt.Println(cfg.Database.Path)
		return 0
	case "init":
		return configInit(flags, args[1:])
	default:
		return fatal("unknown config subcommand %q (print|path|init)", sub)
	}
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

var _ = proto.KindIGD2
var _ = os.Stdout
var _ = scanner.Request{}
