package report

import (
	"testing"

	"plumb/internal/rules"
)

func TestProjectDaysToCritical(t *testing.T) {
	base := rules.Stats{MetricID: "aggr_capacity", SeverityWatch: 80, SeverityCritical: 95, TrendSpanSeconds: 7 * 86400}

	grow := base
	grow.FirstQuarterAvg, grow.LastQuarterAvg = 84, 86 // +2 points over 7 days -> ~0.286/day; 9 points to go
	days, ok := ProjectDaysToCritical(grow)
	if !ok || days < 30 || days > 33 {
		t.Errorf("growing capacity: got (%v,%v), want ~31.5 days", days, ok)
	}

	flat := base
	flat.FirstQuarterAvg, flat.LastQuarterAvg = 86, 86
	if _, ok := ProjectDaysToCritical(flat); ok {
		t.Error("a flat metric has no threshold to project toward")
	}

	low := base
	low.FirstQuarterAvg, low.LastQuarterAvg = 40, 45
	if _, ok := ProjectDaysToCritical(low); ok {
		t.Error("below the watch line a projection is not yet a capacity-planning conversation")
	}

	past := base
	past.FirstQuarterAvg, past.LastQuarterAvg = 94, 96
	if _, ok := ProjectDaysToCritical(past); ok {
		t.Error("already past critical: nothing to project")
	}

	lat := grow
	lat.MetricID = "host_latency"
	if _, ok := ProjectDaysToCritical(lat); ok {
		t.Error("only capacity metrics are projected; latency growth is not a days-to-full question")
	}
}
