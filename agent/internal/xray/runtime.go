package xray

import "github.com/SayukiOvO/chiral/agent/internal/runtimeprovider"

// Keep the direct-Xray package's public names as aliases while the shared
// shapes live at the provider boundary. Existing callers and tests therefore
// keep compiling, and Manager's method signatures are identical to Runtime's.
type Stat = runtimeprovider.Stat
type UserTraffic = runtimeprovider.UserTraffic
type OnlineUser = runtimeprovider.OnlineUser

func UserTrafficFrom(stats []Stat) []UserTraffic {
	return runtimeprovider.UserTrafficFrom(stats)
}

// InstalledVersion is the version the next direct-Xray start would use. For
// the direct provider that is exactly the configured binary version.
func (m *Manager) InstalledVersion() string { return m.BinaryVersion() }

var _ runtimeprovider.Runtime = (*Manager)(nil)
