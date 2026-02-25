// 路径: internal/agent/state.go
package agent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// DefaultStatePath 本地状态文件路径
const DefaultStatePath = "/var/lib/nodectl-agent/state.json"

// State 持久化状态：上次成功上报的时间与累计值
type State struct {
	mu             sync.Mutex
	path           string
	LastReportAt   time.Time `json:"last_report_at"`   // 上次成功上报时间
	LastRXBytes    int64     `json:"last_rx_bytes"`    // 上次上报的累计 RX
	LastTXBytes    int64     `json:"last_tx_bytes"`    // 上次上报的累计 TX
	AccumulatedRX  int64     `json:"accumulated_rx"`   // 跨重启的累计下载量
	AccumulatedTX  int64     `json:"accumulated_tx"`   // 跨重启的累计上传量
	LastResetMonth int       `json:"last_reset_month"` // 上次执行月度重置的月份
}

// NewState 创建状态管理器
func NewState(path string) *State {
	if path == "" {
		path = DefaultStatePath
	}
	return &State{path: path}
}

// Load 从磁盘加载状态文件，不存在则使用默认值
func (s *State) Load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // 文件不存在，使用零值
		}
		return err
	}

	return json.Unmarshal(data, s)
}

// Save 将当前状态写入磁盘
// 使用原子写入（temp → fsync → rename），防止掉电/SIGKILL 导致文件损坏
func (s *State) Save() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 确保目录存在
	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	data, err := json.Marshal(s) // 去掉 MarshalIndent，机器读文件不需缩进
	if err != nil {
		return err
	}

	// 写入临时文件
	tmp := s.path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}

	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}

	// fsync 确保数据落盘
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	f.Close()

	// 原子替换目标文件
	return os.Rename(tmp, s.path)
}

// UpdateOnReport 上报成功后更新状态
func (s *State) UpdateOnReport(rxBytes, txBytes int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.LastReportAt = time.Now()
	s.LastRXBytes = rxBytes
	s.LastTXBytes = txBytes
	s.AccumulatedRX = rxBytes
	s.AccumulatedTX = txBytes
}

// GetAccumulated 获取持久化的累计值
func (s *State) GetAccumulated() (rx, tx int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.AccumulatedRX, s.AccumulatedTX
}

// CheckMonthlyReset 检查是否需要执行月度重置
// resetDay: 1-31 表示每月哪天重置，0 表示不重置
func (s *State) CheckMonthlyReset(resetDay int) bool {
	if resetDay <= 0 {
		return false
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	currentMonth := int(now.Month())

	// 如果当前月份与上次重置月份相同，则不重置
	if s.LastResetMonth == currentMonth {
		return false
	}

	// 检查是否已过重置日
	currentDay := now.Day()
	if currentDay >= resetDay {
		s.LastResetMonth = currentMonth
		s.AccumulatedRX = 0
		s.AccumulatedTX = 0
		return true
	}

	return false
}

// ResetAccumulated 手动重置累计值
func (s *State) ResetAccumulated() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.AccumulatedRX = 0
	s.AccumulatedTX = 0
	s.LastResetMonth = int(time.Now().Month())
}
