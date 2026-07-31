package ruleset

import "fmt"

// Preset is one of the routing configurations ACL4SSR publishes.
//
// Compiled in rather than listed from the network: the set changes on the
// order of years, and an operator choosing one should not have to wait on
// GitHub — least of all on this software, whose users are frequently unable to
// reach it until the proxy they are configuring works.
//
// Everything here is upstream's own claim, read out of the comment block each
// .ini carries, plus counts taken from the file itself. Nothing is editorial:
// a description written from memory is a description that drifts.
type Preset struct {
	Key  string
	Name string
	// Features are upstream's declared capabilities, e.g. 去广告, 自动测速.
	Features []string
	// Groups and Lists are what the file contains: how many policy groups the
	// subscriber will see, and how many rule lists get fetched. The second is
	// the one that costs — a 37-list preset is 37 provider files.
	Groups int
	Lists  int
}

const presetBase = "https://raw.githubusercontent.com/ACL4SSR/ACL4SSR/master/Clash/config/"

// PresetURL is where a built-in preset is fetched from.
func PresetURL(key string) string { return presetBase + key + ".ini" }

// Presets lists the built-ins, the online variants first: those reference rule
// lists by URL and so pick up upstream's edits, while the rest bundle a
// snapshot that only moves when the preset itself does.
func Presets() []Preset {
	return []Preset{
		{Key: "ACL4SSR_Online", Name: "在线版", Groups: 11, Lists: 16, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_Online_AdblockPlus", Name: "在线版 · 增强拦截", Groups: 12, Lists: 19, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_Online_Full", Name: "在线完整版", Groups: 29, Lists: 33, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Full_AdblockPlus", Name: "在线完整版 · 增强拦截", Groups: 33, Lists: 37, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Full_Google", Name: "在线完整版 · Google 分组", Groups: 33, Lists: 36, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Full_MultiMode", Name: "在线完整版 · 多模式", Groups: 31, Lists: 33, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Full_Netflix", Name: "在线完整版 · Netflix 分组", Groups: 31, Lists: 34, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Full_NoAuto", Name: "在线完整版 · 无自动测速", Groups: 28, Lists: 33, Features: []string{"去广告", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Online_Mini", Name: "在线精简版", Groups: 5, Lists: 13, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_AdblockPlus", Name: "在线精简版 · 增强拦截", Groups: 5, Lists: 14, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_Ai", Name: "在线精简版 · AI 分组", Groups: 6, Lists: 15, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_Fallback", Name: "在线精简版 · 故障转移", Groups: 6, Lists: 13, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_MultiCountry", Name: "在线精简版 · 多国家分组", Groups: 10, Lists: 13, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_MultiMode", Name: "在线精简版 · 多模式", Groups: 7, Lists: 13, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Online_Mini_NoAuto", Name: "在线精简版 · 无自动测速", Groups: 4, Lists: 13, Features: []string{"去广告"}},
		{Key: "ACL4SSR_Online_MultiCountry", Name: "在线版 · 多国家分组", Groups: 17, Lists: 18, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_Online_NoAuto", Name: "在线版 · 无自动测速", Groups: 10, Lists: 16, Features: []string{"去广告", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_Online_NoReject", Name: "在线版 · 不拦截广告", Groups: 9, Lists: 13, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR", Name: "标准版", Groups: 10, Lists: 14, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_AdblockPlus", Name: "标准版 · 增强拦截", Groups: 11, Lists: 17, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_BackCN", Name: "回国版", Groups: 5, Lists: 11, Features: []string{"去广告", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_Mini", Name: "精简版", Groups: 5, Lists: 12, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Mini_Fallback", Name: "精简版 · 故障转移", Groups: 6, Lists: 12, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Mini_MultiMode", Name: "精简版 · 多模式", Groups: 7, Lists: 12, Features: []string{"去广告", "自动测速"}},
		{Key: "ACL4SSR_Mini_NoAuto", Name: "精简版 · 无自动测速", Groups: 4, Lists: 12, Features: []string{"去广告"}},
		{Key: "ACL4SSR_NoApple", Name: "标准版 · 无苹果分流", Groups: 9, Lists: 14, Features: []string{"去广告", "自动测速", "微软分流"}},
		{Key: "ACL4SSR_NoAuto", Name: "标准版 · 无自动测速", Groups: 9, Lists: 14, Features: []string{"去广告", "微软分流", "苹果分流"}},
		{Key: "ACL4SSR_NoAuto_NoApple", Name: "标准版 · 无测速无苹果", Groups: 8, Lists: 14, Features: []string{"去广告", "微软分流"}},
		{Key: "ACL4SSR_NoAuto_NoApple_NoMicrosoft", Name: "标准版 · 最少分组", Groups: 7, Lists: 13, Features: []string{"去广告"}},
		{Key: "ACL4SSR_NoMicrosoft", Name: "标准版 · 无微软分流", Groups: 9, Lists: 13, Features: []string{"去广告", "自动测速", "苹果分流"}},
		{Key: "ACL4SSR_WithChinaIp", Name: "标准版 · 增强中国 IP 段", Groups: 10, Lists: 14, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段"}},
		{Key: "ACL4SSR_WithChinaIp_WithGFW", Name: "标准版 · 中国 IP 段 + GFW", Groups: 10, Lists: 15, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强中国IP段", "增强国外GFW"}},
		{Key: "ACL4SSR_WithGFW", Name: "标准版 · GFW 列表", Groups: 10, Lists: 14, Features: []string{"去广告", "自动测速", "微软分流", "苹果分流", "增强国外GFW"}},
	}
}

// FindPreset resolves a key to its entry.
func FindPreset(key string) (Preset, error) {
	for _, p := range Presets() {
		if p.Key == key {
			return p, nil
		}
	}
	return Preset{}, fmt.Errorf("unknown preset %q", key)
}
