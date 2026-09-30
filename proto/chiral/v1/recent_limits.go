package chiralv1

// These aggregate limits keep a recent-observation frame below the default
// 4 MiB gRPC ceiling, including maximum-length credential keys and IPv6 text.
const (
	MaxRecentOnlineUsers      = 4096
	MaxRecentOnlineEmailBytes = 512
	MaxRecentOnlineIPs        = 8192
)
