package api

// VPN gateways: instances running the platform's VPN appliance — remote
// access for people (the VPN app, a username and password each) and
// site-to-site tunnels. Field names are the API's.

// VPNGateway is GET /v1/vpn-gateways/{id}.
type VPNGateway struct {
	ID                      string       `json:"id"`
	InstanceID              string       `json:"instance_id"`
	Name                    *string      `json:"name"`
	Location                *string      `json:"location"`
	PublicIP                *string      `json:"public_ip"`
	Running                 bool         `json:"running"`
	Protocol                string       `json:"protocol"`
	RemoteAccessPool        *string      `json:"remote_access_pool"`
	RemoteAccessPoolIssue   *string      `json:"remote_access_pool_issue"`
	RouteAllTraffic         bool         `json:"route_all_traffic"`
	AllowSharedLogins       bool         `json:"allow_shared_logins"`
	MTU                     int          `json:"mtu"`
	WireGuard               VPNWireGuard `json:"wireguard"`
	PendingChanges          bool         `json:"pending_changes"`
	ApplyMode               string       `json:"apply_mode"`
	ApplyState              string       `json:"apply_state"`
	ApplyOperationID        *string      `json:"apply_operation_id"`
	AppliedAt               *string      `json:"applied_at"`
	LastApplyErrorCode      *string      `json:"last_apply_error_code"`
	UpdateAvailable         bool         `json:"update_available"`
	UsersCount              int          `json:"users_count"`
	SiteConnectionsCount    int          `json:"site_connections_count"`
	IncludedSiteConnections int          `json:"included_site_connections"`
	CreatedAt               *string      `json:"created_at"`
}

// VPNWireGuard is what the far end of a WireGuard site connection needs.
type VPNWireGuard struct {
	PublicKey  *string `json:"public_key"`
	ListenPort int     `json:"listen_port"`
}

// VPNApply is the 202 of POST /v1/vpn-gateways/{id}/apply.
type VPNApply struct {
	GatewayID   string  `json:"gateway_id"`
	Mode        string  `json:"mode"`
	OperationID *string `json:"operation_id"`
	Coalesced   bool    `json:"coalesced"`
}

// VPNProfile is GET /v1/vpn-gateways/{id}/profile: the file people import.
type VPNProfile struct {
	GatewayID string `json:"gateway_id"`
	Format    string `json:"format"`
	Filename  string `json:"filename"`
	Content   string `json:"content"`
}

// VPNUser is a login on a gateway. Password is there once: in the answer to
// a create that let the API generate it.
type VPNUser struct {
	ID        string  `json:"id"`
	GatewayID string  `json:"gateway_id"`
	Username  string  `json:"username"`
	Email     string  `json:"email"`
	Status    string  `json:"status"`
	CreatedAt *string `json:"created_at"`
	UpdatedAt *string `json:"updated_at"`
	Password  *string `json:"password,omitempty"`
}

// VPNSiteConnection is a tunnel to an office or another cloud (read only).
type VPNSiteConnection struct {
	ID              string   `json:"id"`
	GatewayID       string   `json:"gateway_id"`
	Name            string   `json:"name"`
	Protocol        string   `json:"protocol"`
	Status          string   `json:"status"`
	Enabled         bool     `json:"enabled"`
	PeerVendor      string   `json:"peer_vendor"`
	PeerAddress     *string  `json:"peer_address"`
	RemoteNetworks  []string `json:"remote_networks"`
	TunnelState     string   `json:"tunnel_state"`
	ErrorClass      string   `json:"error_class"`
	ErrorDetail     *string  `json:"error_detail"`
	LastHandshakeAt *string  `json:"last_handshake_at"`
	LastUpAt        *string  `json:"last_up_at"`
	BytesIn         string   `json:"bytes_in"`
	BytesOut        string   `json:"bytes_out"`
	AppliedAt       *string  `json:"applied_at"`
	CreatedAt       string   `json:"created_at"`
}
