// Package health 提供上游连通性观测器，用于 readiness 探针判定。
package health

import (
	"sync"
	"sync/atomic"
	"time"
)

// ConnectProbe 在一个滚动窗口内统计 ObserveSuccess / ObserveFailure，
// 让 readiness 接口能基于近期上游连接成功率判断服务是否可对外承接流量。
//
// 实现策略：当窗口（默认 60 秒）过期时，整体重置计数。重置使用 mutex
// 保证并发安全。读写都是 atomic 操作，性能足以扛住每次 ASR/TTS dial 触发的埋点。
type ConnectProbe struct {
	successCount atomic.Int64
	failureCount atomic.Int64
	windowMs     int64
	rollMu       sync.Mutex
	windowStart  time.Time
}

const defaultWindowMs = 60 * 1000

// New 创建一个使用默认 60 秒窗口的 ConnectProbe。
func New() *ConnectProbe {
	return NewWithWindow(defaultWindowMs * time.Millisecond)
}

// NewWithWindow 自定义窗口，主要给测试用。
func NewWithWindow(window time.Duration) *ConnectProbe {
	if window <= 0 {
		window = defaultWindowMs * time.Millisecond
	}
	return &ConnectProbe{
		windowMs:    window.Milliseconds(),
		windowStart: time.Now(),
	}
}

func (p *ConnectProbe) maybeRoll() {
	if time.Since(p.windowStart).Milliseconds() < p.windowMs {
		return
	}
	p.rollMu.Lock()
	defer p.rollMu.Unlock()
	if time.Since(p.windowStart).Milliseconds() < p.windowMs {
		return
	}
	p.successCount.Store(0)
	p.failureCount.Store(0)
	p.windowStart = time.Now()
}

// ObserveSuccess 记录一次上游连接成功。
func (p *ConnectProbe) ObserveSuccess() {
	p.maybeRoll()
	p.successCount.Add(1)
}

// ObserveFailure 记录一次上游连接失败。
func (p *ConnectProbe) ObserveFailure() {
	p.maybeRoll()
	p.failureCount.Add(1)
}

// Snapshot 返回当前窗口内的样本数和成功率。当 samples=0 时 ratio 返回 1.0
// （视为健康，因为没有近期失败信号）。
func (p *ConnectProbe) Snapshot() (samples int64, ratio float64) {
	p.maybeRoll()
	s := p.successCount.Load()
	f := p.failureCount.Load()
	total := s + f
	if total == 0 {
		return 0, 1.0
	}
	return total, float64(s) / float64(total)
}
