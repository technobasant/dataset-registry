package freshness

import (
	"testing"
	"time"
)

func TestClassify(t *testing.T) {
	slo := time.Hour
	for _, c := range []struct {
		staleness time.Duration
		want      Status
	}{
		{0, Healthy},
		{45 * time.Minute, Healthy},
		{45*time.Minute + time.Second, AtRisk},
		{time.Hour, AtRisk},
		{time.Hour + time.Millisecond, Breached},
	} {
		if got := Classify(c.staleness, slo, DefaultAtRiskRatio); got != c.want {
			t.Errorf("staleness %v: got %s, want %s", c.staleness, got, c.want)
		}
	}
	if Classify(time.Hour, 0, DefaultAtRiskRatio) != Unknown {
		t.Error("no SLO means unknown")
	}
}
