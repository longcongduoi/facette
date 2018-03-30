package series

import "time"

// Query represents a time series point query instance.
type Query struct {
	StartTime time.Time
	EndTime   time.Time
	Sample    int
	Series    []QuerySeries
}

// Step returns a step duration according to the query sampling.
func (q *Query) Step() time.Duration {
	return q.EndTime.Sub(q.StartTime) / time.Duration(q.Sample)
}

// QuerySeries represents a series instance in a time series point query.
type QuerySeries struct {
	Origin string
	Source string
	Metric string
}
