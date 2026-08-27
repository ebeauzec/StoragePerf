// Package export turns the metrics Plumb has stored into files a user can
// actually take somewhere else — CSV for spreadsheets, or VictoriaMetrics's
// own JSON-lines format for a full-fidelity backup/migration.
package export

import (
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"plumb/internal/config"
	"plumb/internal/vm"
)

// CSV writes one row per (metric, timestamp) sample across every metric
// defined for the array's vendor, over [start, end]. Long format (not one
// column per metric) so it stays simple regardless of how many metrics a
// vendor defines, and imports cleanly into a pivot table.
func CSV(w io.Writer, client *vm.Client, arr config.Array, metrics []config.MetricDef, start, end time.Time, step time.Duration) error {
	cw := csv.NewWriter(w)
	defer cw.Flush()

	if err := cw.Write([]string{"array_id", "metric_id", "metric_label", "category", "unit", "timestamp_unix", "timestamp_iso", "value"}); err != nil {
		return err
	}

	for _, m := range metrics {
		// same substitution rule as the rules engine
		promql := strings.ReplaceAll(m.Query, "{array}", arr.ID)

		pts, err := client.RangeQuery(promql, start, end, step)
		if err != nil {
			return fmt.Errorf("exporting %s: %w", m.ID, err)
		}
		for _, p := range pts {
			ts := time.Unix(int64(p.Time), 0).UTC()
			row := []string{
				arr.ID, m.ID, m.Label, m.Category, m.Unit,
				strconv.FormatFloat(p.Time, 'f', 0, 64),
				ts.Format(time.RFC3339),
				strconv.FormatFloat(p.Value, 'f', -1, 64),
			}
			if err := cw.Write(row); err != nil {
				return err
			}
		}
	}
	return nil
}
