package api

// routes wires every endpoint. Method-qualified patterns require Go 1.22+.
func (s *Server) routes() {
	m := s.mux

	// ---- public ----
	m.HandleFunc("GET /api/health", s.handleHealth)
	m.HandleFunc("GET /api/meta", s.handleMeta)
	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.handleLogout)
	m.HandleFunc("GET /api/auth/me", s.handleMe)

	// ---- dashboard / stats ----
	m.HandleFunc("GET /api/stats", s.authMiddleware(s.handleStats))
	m.HandleFunc("GET /api/dashboard", s.authMiddleware(s.handleDashboard))

	// ---- rules (host + port management list) ----
	m.HandleFunc("GET /api/rules", s.authMiddleware(s.handleListRules))
	m.HandleFunc("POST /api/rules", s.writeMiddleware(s.handleCreateRule))
	m.HandleFunc("GET /api/rules/{id}", s.authMiddleware(s.handleGetRule))
	m.HandleFunc("PUT /api/rules/{id}", s.writeMiddleware(s.handleUpdateRule))
	m.HandleFunc("PATCH /api/rules/{id}", s.writeMiddleware(s.handlePatchRule))
	m.HandleFunc("DELETE /api/rules/{id}", s.writeMiddleware(s.handleDeleteRule))
	m.HandleFunc("POST /api/rules/bulk", s.writeMiddleware(s.handleBulkRules))
	m.HandleFunc("POST /api/rules/{id}/toggle", s.writeMiddleware(s.handleToggleRule))
	m.HandleFunc("POST /api/rules/{id}/apply", s.writeMiddleware(s.handleApplyRule))
	m.HandleFunc("GET /api/rules/{id}/mapping", s.authMiddleware(s.handleRuleMapping))

	// ---- discovered ports ----
	m.HandleFunc("GET /api/ports", s.authMiddleware(s.handleListPorts))
	m.HandleFunc("POST /api/ports/add", s.writeMiddleware(s.handlePortsAdd))
	m.HandleFunc("POST /api/ports/{id}/action", s.writeMiddleware(s.handlePortAction))
	m.HandleFunc("PATCH /api/ports/{id}", s.writeMiddleware(s.handlePatchPort))
	m.HandleFunc("DELETE /api/ports/{id}", s.writeMiddleware(s.handleDeletePort))
	m.HandleFunc("POST /api/ports/bulk", s.writeMiddleware(s.handleBulkPorts))
	m.HandleFunc("POST /api/ports/promote", s.writeMiddleware(s.handlePromoteAll))

	// ---- scans ----
	m.HandleFunc("GET /api/scans", s.authMiddleware(s.handleListScans))
	m.HandleFunc("POST /api/scans", s.writeMiddleware(s.handleStartScan))
	m.HandleFunc("GET /api/scans/{id}", s.authMiddleware(s.handleGetScan))
	m.HandleFunc("DELETE /api/scans/{id}", s.writeMiddleware(s.handleDeleteScan))
	m.HandleFunc("POST /api/scans/{id}/cancel", s.writeMiddleware(s.handleCancelScan))
	m.HandleFunc("GET /api/scans/{id}/results", s.authMiddleware(s.handleScanResults))
	m.HandleFunc("POST /api/scans/preview", s.writeMiddleware(s.handleScanPreview))

	// ---- policies ----
	m.HandleFunc("GET /api/policies", s.authMiddleware(s.handleListPolicies))
	m.HandleFunc("POST /api/policies", s.writeMiddleware(s.handleCreatePolicy))
	m.HandleFunc("PUT /api/policies/{id}", s.writeMiddleware(s.handleUpdatePolicy))
	m.HandleFunc("DELETE /api/policies/{id}", s.writeMiddleware(s.handleDeletePolicy))

	// ---- auto monitor ----
	m.HandleFunc("GET /api/monitor", s.authMiddleware(s.handleMonitorStatus))
	m.HandleFunc("POST /api/monitor/start", s.writeMiddleware(s.handleMonitorStart))
	m.HandleFunc("POST /api/monitor/stop", s.writeMiddleware(s.handleMonitorStop))

	// ---- gateways / mappings ----
	m.HandleFunc("GET /api/gateways", s.authMiddleware(s.handleListGateways))
	m.HandleFunc("POST /api/gateways/discover", s.writeMiddleware(s.handleDiscover))
	m.HandleFunc("POST /api/gateways/{id}/preferred", s.writeMiddleware(s.handleGatewayPreferred))
	m.HandleFunc("POST /api/gateways/{id}/enable", s.writeMiddleware(s.handleGatewayEnable))
	m.HandleFunc("DELETE /api/gateways/{id}", s.writeMiddleware(s.handleGatewayDelete))
	m.HandleFunc("GET /api/gateways/mappings", s.authMiddleware(s.handleGatewayMappings))
	m.HandleFunc("GET /api/external-ip", s.authMiddleware(s.handleExternalIP))
	m.HandleFunc("POST /api/mapper/reconcile", s.writeMiddleware(s.handleReconcile))
	m.HandleFunc("POST /api/mapper/cleanup", s.writeMiddleware(s.handleCleanup))

	// ---- hosts ----
	m.HandleFunc("GET /api/hosts", s.authMiddleware(s.handleListHosts))
	m.HandleFunc("POST /api/hosts/scan", s.writeMiddleware(s.handleHostScan))
	// Per-host monitoring: policies bound to the host's MAC so a DHCP address
	// change does not break them. A host may have several such policies.
	m.HandleFunc("GET /api/hosts/{id}/policies", s.authMiddleware(s.handleListHostPolicies))
	m.HandleFunc("POST /api/hosts/{id}/policies", s.writeMiddleware(s.handleCreateHostPolicy))
	m.HandleFunc("PATCH /api/hosts/{id}", s.writeMiddleware(s.handlePatchHost))
	m.HandleFunc("DELETE /api/hosts/{id}", s.writeMiddleware(s.handleDeleteHost))

	// ---- schedules ----
	m.HandleFunc("GET /api/schedules", s.authMiddleware(s.handleListSchedules))
	m.HandleFunc("POST /api/schedules", s.writeMiddleware(s.handleCreateSchedule))
	m.HandleFunc("PUT /api/schedules/{id}", s.writeMiddleware(s.handleUpdateSchedule))
	m.HandleFunc("DELETE /api/schedules/{id}", s.writeMiddleware(s.handleDeleteSchedule))

	// ---- network helpers ----
	m.HandleFunc("GET /api/network/interfaces", s.authMiddleware(s.handleInterfaces))

	// ---- system ----
	m.HandleFunc("GET /api/system/info", s.authMiddleware(s.handleSystemInfo))
	m.HandleFunc("GET /api/settings", s.authMiddleware(s.handleGetSettings))
	m.HandleFunc("PUT /api/settings", s.adminMiddleware(s.handlePutSettings))
	m.HandleFunc("PATCH /api/settings", s.adminMiddleware(s.handlePatchSettings))
	m.HandleFunc("GET /api/events", s.authMiddleware(s.handleListEvents))
	m.HandleFunc("POST /api/events/read", s.writeMiddleware(s.handleEventsRead))
	m.HandleFunc("DELETE /api/events", s.writeMiddleware(s.handleClearEvents))
	m.HandleFunc("GET /api/audit", s.adminMiddleware(s.handleAudit))
	m.HandleFunc("GET /api/logs/stream", s.authMiddleware(s.handleEventStream))
	m.HandleFunc("GET /api/export", s.adminMiddleware(s.handleExport))
	m.HandleFunc("POST /api/import", s.adminMiddleware(s.handleImport))

	// ---- users ----
	m.HandleFunc("GET /api/users", s.adminMiddleware(s.handleListUsers))
	m.HandleFunc("POST /api/users", s.adminMiddleware(s.handleCreateUser))
	m.HandleFunc("PUT /api/users/{id}", s.adminMiddleware(s.handleUpdateUser))
	m.HandleFunc("DELETE /api/users/{id}", s.adminMiddleware(s.handleDeleteUser))
	m.HandleFunc("POST /api/users/{id}/token", s.adminMiddleware(s.handleUserToken))
	m.HandleFunc("POST /api/account/password", s.writeMiddleware(s.handleChangeOwnPassword))

	// ---- SPA ----
	m.HandleFunc("/", s.serveStatic)
}
