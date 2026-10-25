package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/ekko-studio/natpilot/internal/app"
	"github.com/ekko-studio/natpilot/internal/model"
	"github.com/ekko-studio/natpilot/internal/store"
)

// ---- rule ----

func cmdRule(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 {
		return ruleList(ctx, a, nil, asJSON)
	}
	switch args[0] {
	case "list", "ls":
		return ruleList(ctx, a, args[1:], asJSON)
	case "add", "create":
		return ruleAdd(ctx, a, args[1:], asJSON)
	case "set", "update", "edit":
		return ruleSet(ctx, a, args[1:], asJSON)
	case "rm", "delete", "remove":
		return ruleRemove(ctx, a, args[1:], asJSON)
	case "enable", "on":
		return ruleToggle(ctx, a, args[1:], true, asJSON)
	case "disable", "off":
		return ruleToggle(ctx, a, args[1:], false, asJSON)
	case "apply":
		return ruleApply(ctx, a, args[1:], asJSON)
	case "tag":
		return ruleTag(ctx, a, args[1:], asJSON)
	default:
		return fatal("unknown rule subcommand %q (list|add|set|rm|enable|disable|apply|tag)", args[0])
	}
}

func ruleList(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	kv, _ := parseKV(args)
	f := store.RuleFilter{
		Query:    kv["q"],
		Protocol: kv["protocol"],
		Limit:    flagInt(kv, "limit", 200),
		Sort:     kv["sort"],
		Order:    kv["order"],
	}
	if t := flagList(kv, "tags"); len(t) > 0 {
		f.Tags = t
	}
	if _, ok := kv["enabled"]; ok {
		v := flagBool(kv, "enabled", true)
		f.Enabled = &v
	}
	rules, total, err := a.Store.ListRules(f)
	if err != nil {
		return fatal("%v", err)
	}
	records, _ := a.Store.MappingsByRule()
	if asJSON {
		printJSON(map[string]any{"rules": rules, "total": total})
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATE\tTARGET\tEXTERNAL\tPROTO\tTAGS\tNAME")
	for _, r := range rules {
		state := "disabled"
		if r.Enabled {
			state = "enabled"
		}
		ext := "-"
		if rec, ok := records[r.ID]; ok {
			if rec.ExternalPort > 0 {
				ext = fmt.Sprintf("%s:%d", orDash(rec.ExternalIP), rec.ExternalPort)
			}
			if rec.Status != "" && rec.Status != model.MapStatusActive {
				state += "/" + rec.Status
			}
		}
		target := fmt.Sprintf("%s:%d", r.Host, r.Port)
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.ID, state, target, ext, r.Protocol, strings.Join(r.Tags, ","), r.Name)
	}
	tw.Flush()
	fmt.Printf("\n%d rule(s)\n", total)
	return 0
}

func orDash(s string) string {
	if s == "" {
		return "?"
	}
	return s
}

func ruleAdd(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	kv, _ := parseKV(args)
	port := flagInt(kv, "port", 0)
	if port < 1 || port > 65535 {
		return fatal("--port is required and must be 1..65535")
	}
	host := kv["host"]
	if host == "" {
		host = "127.0.0.1"
	}
	proto := strings.ToLower(kv["protocol"])
	if proto == "" {
		proto = model.ProtoTCP
	}
	rule := &model.Rule{
		Name:     kv["name"],
		Host:     host,
		Port:     port,
		ExtPort:  flagInt(kv, "ext-port", 0),
		Protocol: proto,
		Tags:     model.StringList(flagList(kv, "tags")),
		Note:     kv["note"],
		Enabled:  flagBool(kv, "enable", true),
		Source:   "cli",
	}
	if rule.Tags == nil {
		rule.Tags = model.StringList{}
	}
	if existing, _ := a.Store.GetRuleByKey(host, port, proto); existing != nil {
		return fatal("a rule for %s:%d (%s) already exists (id %d)", host, port, proto, existing.ID)
	}
	id, err := a.Store.CreateRule(rule)
	if err != nil {
		return fatal("%v", err)
	}
	rule.ID = id
	sum, aerr := a.Mapper.ApplyRule(ctx, *rule)
	if asJSON {
		out := map[string]any{"ok": aerr == nil, "rule": rule, "result": sum}
		if aerr != nil {
			out["error"] = aerr.Error()
		}
		printJSON(out)
		if aerr != nil {
			return 1
		}
		return 0
	}
	fmt.Printf("created rule #%d for %s:%d (%s), enabled=%v\n", id, host, port, proto, rule.Enabled)
	if aerr != nil {
		fmt.Fprintf(os.Stderr, "warning: provisioning failed: %v\n", aerr)
		return 1
	}
	fmt.Printf("mapping: %s\n", sum.String())
	return 0
}

func ruleSet(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	id, rest, ok := leadingID(args)
	if !ok {
		return fatal("usage: rule set <id> [--port N] [--host H] [--ext-port N] [--tags a,b] [--note S] [--enable|--disable]")
	}
	kv, _ := parseKV(rest)
	rule, err := a.Store.GetRule(id)
	if err != nil || rule == nil {
		return fatal("rule #%d not found", id)
	}
	before := rule.Clone()
	if v, ok := kv["port"]; ok {
		n, _ := strconv.Atoi(v)
		rule.Port = n
	}
	if v := kv["host"]; v != "" {
		rule.Host = v
	}
	if v, ok := kv["ext-port"]; ok {
		n, _ := strconv.Atoi(v)
		rule.ExtPort = n
	}
	if v := kv["protocol"]; v != "" {
		rule.Protocol = strings.ToLower(v)
	}
	if v := kv["name"]; v != "" {
		rule.Name = v
	}
	if v, ok := kv["note"]; ok {
		rule.Note = v
	}
	if t := flagList(kv, "tags"); len(t) > 0 {
		rule.Tags = model.StringList(t)
	}
	if _, ok := kv["enable"]; ok {
		rule.Enabled = flagBool(kv, "enable", true)
	}
	if _, ok := kv["disable"]; ok {
		rule.Enabled = false
	}
	if err := a.Store.UpdateRule(rule); err != nil {
		return fatal("%v", err)
	}
	needsReprovision := before.Port != rule.Port || before.Host != rule.Host ||
		before.Protocol != rule.Protocol || before.ExtPort != rule.ExtPort
	var sum any
	var applyErr string
	if !rule.Enabled {
		if err := a.Mapper.RemoveMapping(ctx, *rule); err != nil {
			applyErr = err.Error()
		}
	} else if needsReprovision || before.Enabled != rule.Enabled {
		if needsReprovision {
			_ = a.Mapper.RemoveMapping(ctx, before)
		}
		s, err := a.Mapper.ApplyRule(ctx, *rule)
		sum = s
		if err != nil {
			applyErr = err.Error()
		}
	}
	if asJSON {
		out := map[string]any{"ok": applyErr == "", "rule": rule, "result": sum}
		if applyErr != "" {
			out["error"] = applyErr
		}
		printJSON(out)
		if applyErr != "" {
			return 1
		}
		return 0
	}
	fmt.Printf("updated rule #%d\n", id)
	if applyErr != "" {
		fmt.Fprintf(os.Stderr, "warning: %s\n", applyErr)
		return 1
	}
	return 0
}

func ruleRemove(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	ids := leadingIDs(args)
	if len(ids) == 0 {
		return fatal("usage: rule rm <id> [id...]")
	}
	removed := 0
	for _, id := range ids {
		rule, err := a.Store.GetRule(id)
		if err != nil || rule == nil {
			continue
		}
		if rule.Enabled {
			if err := a.Mapper.RemoveMapping(ctx, *rule); err != nil {
				fmt.Fprintf(os.Stderr, "warning: gateway teardown for rule #%d failed: %v\n", id, err)
			}
		}
		if err := a.Store.DeleteRule(id); err == nil {
			_ = a.Store.DeleteMappingsForRule(id)
			removed++
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "deleted": removed})
		return 0
	}
	fmt.Printf("deleted %d rule(s)\n", removed)
	return 0
}

func ruleToggle(ctx context.Context, a *app.App, args []string, enabled bool, asJSON bool) int {
	ids := leadingIDs(args)
	if len(ids) == 0 {
		return fatal("usage: rule enable|disable <id> [id...]")
	}
	if _, err := a.Store.SetRulesEnabled(ids, enabled); err != nil {
		return fatal("%v", err)
	}
	ok, failed := 0, 0
	for _, id := range ids {
		rule, _ := a.Store.GetRule(id)
		if rule == nil {
			continue
		}
		if _, err := a.Mapper.ApplyRule(ctx, *rule); err != nil {
			failed++
		} else {
			ok++
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": failed == 0, "enabled": enabled, "applied": ok, "failed": failed})
		return 0
	}
	fmt.Printf("%d rule(s) %s, %d mapping(s) applied, %d failed\n", len(ids), map[bool]string{true: "enabled", false: "disabled"}[enabled], ok, failed)
	return 0
}

func ruleApply(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	ids := leadingIDs(args)
	if len(ids) == 0 {
		return fatal("usage: rule apply <id> [id...]")
	}
	type result struct {
		ID  int64  `json:"id"`
		OK  bool   `json:"ok"`
		Err string `json:"error,omitempty"`
	}
	results := []result{}
	for _, id := range ids {
		rule, _ := a.Store.GetRule(id)
		if rule == nil {
			results = append(results, result{ID: id, OK: false, Err: "not found"})
			continue
		}
		_, err := a.Mapper.ApplyRule(ctx, *rule)
		results = append(results, result{ID: id, OK: err == nil, Err: errString(err)})
	}
	if asJSON {
		printJSON(map[string]any{"results": results})
		return 0
	}
	for _, r := range results {
		status := "ok"
		if !r.OK {
			status = "failed: " + r.Err
		}
		fmt.Printf("rule #%d: %s\n", r.ID, status)
	}
	return 0
}

func ruleTag(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	id, rest, ok := leadingID(args)
	if !ok {
		return fatal("usage: rule tag <id> [--add a,b] [--remove c] [--note S]")
	}
	kv, _ := parseKV(rest)
	rule, err := a.Store.GetRule(id)
	if err != nil || rule == nil {
		return fatal("rule #%d not found", id)
	}
	set := map[string]bool{}
	for _, t := range rule.Tags {
		set[t] = true
	}
	for _, t := range flagList(kv, "add") {
		set[t] = true
	}
	for _, t := range flagList(kv, "remove") {
		delete(set, t)
	}
	tags := model.StringList{}
	for t := range set {
		tags = append(tags, t)
	}
	rule.Tags = tags
	if v, ok := kv["note"]; ok {
		rule.Note = v
	}
	if err := a.Store.UpdateRule(rule); err != nil {
		return fatal("%v", err)
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "rule": rule})
		return 0
	}
	fmt.Printf("rule #%d tags: %s\n", id, strings.Join(tags, ","))
	return 0
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---- ports ----

func cmdPorts(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 {
		return portsList(ctx, a, nil, asJSON)
	}
	switch args[0] {
	case "list", "ls":
		return portsList(ctx, a, args[1:], asJSON)
	case "add":
		return portsAdd(ctx, a, args[1:], asJSON)
	case "ignore":
		return portsDecision(ctx, a, args[1:], model.ActionIgnore, asJSON)
	case "watch":
		return portsDecision(ctx, a, args[1:], model.ActionWatch, asJSON)
	case "delete", "rm":
		return portsDecision(ctx, a, args[1:], model.ActionDelete, asJSON)
	case "clear":
		return portsClear(ctx, a, args[1:], asJSON)
	default:
		return fatal("unknown ports subcommand %q (list|add|ignore|watch|delete|clear)", args[0])
	}
}

func portsList(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	kv, _ := parseKV(args)
	f := store.PortStatusFilter{
		Host:       kv["host"],
		Query:      kv["q"],
		TargetKind: kv["target-kind"],
		Limit:      flagInt(kv, "limit", 500),
	}
	if v := kv["status"]; v != "" {
		f.Statuses = strings.Split(v, ",")
	}
	if _, ok := kv["open-only"]; ok {
		f.OnlyOpen = flagBool(kv, "open-only", true)
	}
	ports, total, err := a.Store.ListPorts(f)
	if err != nil {
		return fatal("%v", err)
	}
	counts, _ := a.Store.CountPortsByStatus()
	if asJSON {
		printJSON(map[string]any{"ports": ports, "total": total, "counts": counts})
		return 0
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tSTATUS\tHOST\tPORT\tPROTO\tSERVICE\tPROCESS\tBANNER")
	for _, p := range ports {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\t%s\t%s\t%s\n",
			p.ID, p.Status, p.Host, p.Port, p.Protocol, p.Service, truncateCell(p.Process, 28), truncateCell(p.Banner, 34))
	}
	tw.Flush()
	fmt.Printf("\n%d port(s)", total)
	if len(counts) > 0 {
		fmt.Printf("  [")
		first := true
		for st, n := range counts {
			if !first {
				fmt.Print(", ")
			}
			fmt.Printf("%s=%d", st, n)
			first = false
		}
		fmt.Print("]")
	}
	fmt.Println()
	return 0
}

// truncateCell shortens a table cell to n runes. Slicing bytes would cut
// multi-byte characters in half, which garbles CJK process names on Windows and
// breaks the tabwriter column widths.
func truncateCell(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return string(r[:n-1]) + "…"
}

func portsAdd(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	kv, rest := parseKV(args)
	ids := leadingIDs(rest)
	if len(ids) == 0 {
		return fatal("usage: ports add <port-id> [id...] [--tags a,b]")
	}
	added, addedRules, applied, failed := 0, 0, 0, 0
	for _, id := range ids {
		port, err := a.Store.GetPort(id)
		if err != nil || port == nil {
			failed++
			continue
		}
		rule := &model.Rule{
			Name:     fmt.Sprintf("%s %s:%d", strings.ToUpper(port.Protocol), port.Host, port.Port),
			Host:     port.Host,
			Port:     port.Port,
			Protocol: port.Protocol,
			Tags:     model.StringList(flagList(kv, "tags")),
			Note:     kv["note"],
			Enabled:  true,
			Source:   "scan",
		}
		if rule.Tags == nil {
			rule.Tags = model.StringList{}
		}
		if existing, _ := a.Store.GetRuleByKey(port.Host, port.Port, port.Protocol); existing != nil {
			_ = a.Store.MarkPortAdded(port.ID, existing.ID)
			added++
			continue
		}
		rid, err := a.Store.CreateRule(rule)
		if err != nil {
			failed++
			continue
		}
		rule.ID = rid
		_ = a.Store.MarkPortAdded(port.ID, rid)
		added++
		addedRules++
		if _, err := a.Mapper.ApplyRule(ctx, *rule); err == nil {
			applied++
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": failed == 0, "added": added, "rules_created": addedRules, "applied": applied, "failed": failed})
		return 0
	}
	fmt.Printf("added %d port(s), %d new rule(s), %d applied, %d failed\n", added, addedRules, applied, failed)
	return 0
}

func portsDecision(ctx context.Context, a *app.App, args []string, decision string, asJSON bool) int {
	ids := leadingIDs(args)
	if len(ids) == 0 {
		return fatal("usage: ports %s <port-id> [id...]", decision)
	}
	done, failed := 0, 0
	for _, id := range ids {
		port, err := a.Store.GetPort(id)
		if err != nil || port == nil {
			failed++
			continue
		}
		switch decision {
		case model.ActionIgnore:
			if err := a.Store.MarkPortIgnored(id); err == nil {
				done++
			} else {
				failed++
			}
		case model.ActionWatch:
			if err := a.Store.SetPortStatus(id, model.StatusNotAdded, model.ActionWatch); err == nil {
				done++
			} else {
				failed++
			}
		case model.ActionDelete:
			if port.RuleID > 0 {
				if rule, _ := a.Store.GetRule(port.RuleID); rule != nil {
					_ = a.Mapper.RemoveMapping(ctx, *rule)
					_ = a.Store.DeleteRule(rule.ID)
					_ = a.Store.DeleteMappingsForRule(rule.ID)
				}
			}
			if err := a.Store.DeletePort(id); err == nil {
				done++
			} else {
				failed++
			}
		}
	}
	if asJSON {
		printJSON(map[string]any{"ok": failed == 0, "action": decision, "done": done, "failed": failed})
		return 0
	}
	fmt.Printf("%s: %d done, %d failed\n", decision, done, failed)
	return 0
}

func portsClear(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	kv, _ := parseKV(args)
	statuses := flagList(kv, "status")
	if len(statuses) == 0 {
		statuses = []string{model.StatusClosed, model.StatusSourceGone}
	}
	n, err := a.Store.DeletePortsWhere(statuses, kv["host"])
	if err != nil {
		return fatal("%v", err)
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "deleted": n, "statuses": statuses})
		return 0
	}
	fmt.Printf("cleared %d port(s) with status %s\n", n, strings.Join(statuses, ","))
	return 0
}

// ---- scan ----

func cmdScan(ctx context.Context, a *app.App, args []string, asJSON bool) int {
	if len(args) == 0 {
		return fatal("usage: scan local|cidr <CIDR> [--ports 1-1024] [--add-open] [--wait]")
	}
	kind := args[0]
	kv, rest := parseKV(args[1:])
	portsSpec := kv["ports"]
	if portsSpec == "" {
		portsSpec = a.Config.Scanner.DefaultPorts
	}
	req := scanRequestFromKV(kind, rest, kv, portsSpec)
	run, err := a.Scanner.Run(ctx, req)
	if err != nil {
		if asJSON {
			printJSON(map[string]any{"ok": false, "error": err.Error(), "scan": run})
		}
		return fatal("%v", err)
	}
	if flagBool(kv, "add-open", false) {
		added, applied := promoteOpenPorts(ctx, a, run.ID, kv)
		if asJSON {
			printJSON(map[string]any{"ok": true, "scan": run, "added": added, "applied": applied})
			return 0
		}
		fmt.Printf("scan #%d finished: %d open, %d new; added %d rule(s), %d applied\n",
			run.ID, run.Open, run.NewCount, added, applied)
		return 0
	}
	if asJSON {
		printJSON(map[string]any{"ok": true, "scan": run})
		return 0
	}
	// run.Message already carries the counters, so it is printed as-is.
	fmt.Printf("scan #%d finished: %s\n", run.ID, run.Message)
	ports, _, _ := a.Store.ListPorts(store.PortStatusFilter{ScanID: run.ID, Limit: 200})
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "HOST\tPORT\tPROTO\tSTATUS\tSERVICE\tPROCESS\tBANNER")
	for _, p := range ports {
		fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\t%s\n", p.Host, p.Port, p.Protocol, p.Status, p.Service,
			truncateCell(p.Process, 24), truncateCell(p.Banner, 30))
	}
	tw.Flush()
	return 0
}
