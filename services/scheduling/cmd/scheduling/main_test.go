package main

import (
	"testing"

	"github.com/BenjaminSRussell/passage/services/scheduling/internal/api"
)

func TestLoadConfig_DefaultsAndRanges(t *testing.T) {
	cases := []struct {
		name    string
		env     map[string]string
		want    api.Config
		wantErr bool
	}{
		{"defaults", map[string]string{}, api.DefaultConfig, false},
		{"horizon override", map[string]string{"SCHED_HORIZON_DAYS": "90"}, api.Config{HorizonDays: 90, BottleneckPct: 90, BottleneckWeek: 3}, false},
		{"horizon below range", map[string]string{"SCHED_HORIZON_DAYS": "10"}, api.Config{}, true},
		{"bottleneck not a number", map[string]string{"SCHED_BOTTLENECK_PCT": "high"}, api.Config{}, true},
		{"bottleneck weeks override", map[string]string{"SCHED_BOTTLENECK_WEEKS": "2"}, api.Config{HorizonDays: 365, BottleneckPct: 90, BottleneckWeek: 2}, false},
		{"bottleneck weeks above range", map[string]string{"SCHED_BOTTLENECK_WEEKS": "13"}, api.Config{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := loadConfig(func(k string) string { return c.env[k] })
			if (err != nil) != c.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Fatalf("config = %+v, want %+v", got, c.want)
			}
		})
	}
}
