package operation_setting

import (
	"os"
	"strconv"

	"github.com/QuantumNous/new-api/setting/config"
)

type MonitorSetting struct {
	AutoTestChannelEnabled bool    `json:"auto_test_channel_enabled"`
	AutoTestChannelMinutes float64 `json:"auto_test_channel_minutes"`
	// AutoTestOnlyAutoDisabled: when true, the scheduled channel test only probes
	// auto-disabled (status=3) channels to recover and re-enable those that pass,
	// and skips currently-enabled channels. Avoids synthetic test load on healthy
	// channels and prevents a transient test failure from auto-disabling them. The
	// manual "test all channels" button is unaffected. Default false = test all.
	AutoTestOnlyAutoDisabled bool `json:"auto_test_only_auto_disabled"`
}

// 默认配置
var monitorSetting = MonitorSetting{
	AutoTestChannelEnabled:   false,
	AutoTestChannelMinutes:   10,
	AutoTestOnlyAutoDisabled: false,
}

func init() {
	// 注册到全局配置管理器
	config.GlobalConfig.Register("monitor_setting", &monitorSetting)
}

func GetMonitorSetting() *MonitorSetting {
	if os.Getenv("CHANNEL_TEST_FREQUENCY") != "" {
		frequency, err := strconv.Atoi(os.Getenv("CHANNEL_TEST_FREQUENCY"))
		if err == nil && frequency > 0 {
			monitorSetting.AutoTestChannelEnabled = true
			monitorSetting.AutoTestChannelMinutes = float64(frequency)
		}
	}
	return &monitorSetting
}
