// Package collector samples system metrics for heartbeats (gopsutil). Xray
// traffic stats via StatsService are added in M3.
package collector

import (
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/mem"
	psnet "github.com/shirou/gopsutil/v4/net"

	chiralv1 "github.com/SayukiOvO/chiral/proto/chiral/v1"
)

type Collector struct {
	mu        sync.Mutex
	lastNetAt time.Time
	lastNetTx uint64
	lastNetRx uint64
	netPrimed bool
}

func New() *Collector {
	c := &Collector{}
	// Prime the deltas so the first real sample is meaningful.
	cpu.Percent(0, false)
	c.sampleNet()
	return c
}

// Sample fills the system-metric fields of a Heartbeat. Xray state and config
// version are the caller's business. Individual probe failures leave their
// fields zero rather than failing the heartbeat.
func (c *Collector) Sample() *chiralv1.Heartbeat {
	hb := &chiralv1.Heartbeat{}
	if pcts, err := cpu.Percent(0, false); err == nil && len(pcts) > 0 {
		hb.CpuPercent = pcts[0]
	}
	if vm, err := mem.VirtualMemory(); err == nil {
		hb.MemUsedBytes = vm.Used
		hb.MemTotalBytes = vm.Total
	}
	if du, err := disk.Usage("/"); err == nil {
		hb.DiskUsedBytes = du.Used
		hb.DiskTotalBytes = du.Total
	}
	hb.NetTxBps, hb.NetRxBps = c.sampleNet()
	return hb
}

// sampleNet returns throughput since the previous call, in bytes/second.
func (c *Collector) sampleNet() (tx, rx uint64) {
	counters, err := psnet.IOCounters(false)
	if err != nil || len(counters) == 0 {
		return 0, 0
	}
	now := time.Now()
	curTx, curRx := counters[0].BytesSent, counters[0].BytesRecv

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.netPrimed {
		if secs := now.Sub(c.lastNetAt).Seconds(); secs > 0 && curTx >= c.lastNetTx && curRx >= c.lastNetRx {
			tx = uint64(float64(curTx-c.lastNetTx) / secs)
			rx = uint64(float64(curRx-c.lastNetRx) / secs)
		}
	}
	c.lastNetAt, c.lastNetTx, c.lastNetRx, c.netPrimed = now, curTx, curRx, true
	return tx, rx
}
