// +build !disable_connector_prometheus

package connector

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"facette/catalog"
	"facette/pattern"
	"facette/series"

	"github.com/facette/logger"
	"github.com/facette/maputil"

	prometheus "github.com/prometheus/client_golang/api"
	promapi "github.com/prometheus/client_golang/api/prometheus/v1"
	prommodel "github.com/prometheus/common/model"
)

var (
	defaultSourceLabel    = "instance"
	defaultExcludedLabels = []string{
		"glob:__*__",
		"glob:exported_*",
		"le",
		"tags",
	}
	defaultLabelGlue = "."
)

type prometheusMetric struct {
	metric string
	label  prommodel.LabelPair
}

// prometheusConnector implements the connector handler for a Prometheus instance.
type prometheusConnector struct {
	name           string
	url            string
	timeout        int
	allowInsecure  bool
	client         prometheus.Client
	sourceLabel    string
	excludedLabels []string
	labelGlue      string
	metrics        map[string]*prometheusMetric

	log *logger.Logger
}

func init() {
	connectors["prometheus"] = func(name string, settings *maputil.Map, log *logger.Logger) (Connector, error) {
		var err error

		c := &prometheusConnector{
			name: name,
			log:  log,
		}

		// Load provider configuration
		if c.url, err = settings.GetString("url", ""); err != nil {
			return nil, err
		} else if c.url == "" {
			return nil, ErrMissingConnectorSetting("url")
		}
		normalizeURL(&c.url)

		if c.timeout, err = settings.GetInt("timeout", connectorDefaultTimeout); err != nil {
			return nil, err
		}

		if c.allowInsecure, err = settings.GetBool("allow_insecure_tls", false); err != nil {
			return nil, err
		}

		// Check remote instance URL
		if _, err := url.Parse(c.url); err != nil {
			return nil, fmt.Errorf("unable to parse URL: %s", err)
		}

		if c.sourceLabel, err = settings.GetString("source_label", defaultSourceLabel); err != nil {
			return nil, err
		}

		if c.excludedLabels, err = settings.GetStringSlice("excluded_labels", defaultExcludedLabels); err != nil {
			return nil, err
		}

		if c.labelGlue, err = settings.GetString("label_glue", defaultLabelGlue); err != nil {
			return nil, err
		}

		if c.client, err = prometheus.NewClient(prometheus.Config{Address: c.url}); err != nil {
			return nil, fmt.Errorf("unable to create client: %s", err)
		}

		return c, nil
	}
}

// Name returns the name of the current connector.
func (c *prometheusConnector) Name() string {
	return c.name
}

// Refresh triggers the connector data refresh.
func (c *prometheusConnector) Refresh(output chan<- *catalog.Record) error {
	c.metrics = make(map[string]*prometheusMetric)

	api := promapi.NewAPI(c.client)

	series, err := api.Query(context.Background(), `{job=~".+"}`, time.Now())
	if err != nil {
		return err
	}

	for _, sample := range series.(prommodel.Vector) {
		records, err := c.buildRecords(&sample.Metric)
		if err != nil {
			return err
		}

		for _, r := range records {
			output <- r
		}
	}

	return nil
}

// Points retrieves the time series data according to the query parameters and a time interval.
func (c *prometheusConnector) Points(q *series.Query) ([]series.Series, error) {
	result := []series.Series{}

	api := promapi.NewAPI(c.client)

	for _, qs := range q.Series {
		c.log.Debug("sending query to Prometheus server: %s{%s=%q,%s=%q}",
			c.metrics[qs.Source+qs.Metric].metric,
			c.metrics[qs.Source+qs.Metric].label.Name,
			c.metrics[qs.Source+qs.Metric].label.Value,
			c.sourceLabel,
			qs.Source,
		)

		res, err := api.QueryRange(
			context.Background(),
			fmt.Sprintf("%s{%s=%q,%s=%q}",
				c.metrics[qs.Source+qs.Metric].metric,
				c.metrics[qs.Source+qs.Metric].label.Name,
				c.metrics[qs.Source+qs.Metric].label.Value,
				c.sourceLabel,
				qs.Source,
			),
			promapi.Range{
				Start: q.StartTime,
				End:   q.EndTime,
				Step:  q.Step(),
			},
		)
		if err != nil {
			c.log.Error("%s", err.Error())
			return nil, err
		}

		for _, sample := range res.(prommodel.Matrix) {
			s := series.Series{}
			for _, value := range sample.Values {
				s.Points = append(s.Points, series.Point{
					Time:  value.Timestamp.Time(),
					Value: series.Value(value.Value),
				})
			}

			result = append(result, s)
		}
	}

	return result, nil
}

func (c *prometheusConnector) buildRecords(metric *prommodel.Metric) ([]*catalog.Record, error) {
	records := make([]*catalog.Record, 0)

	if _, ok := (*metric)[prommodel.LabelName(c.sourceLabel)]; !ok {
		return nil, fmt.Errorf("source label %q not found in metric", c.sourceLabel)
	}

	for k, v := range *metric {
		if string(k) == "__name__" || string(k) == c.sourceLabel {
			continue
		}

		metricName := string((*metric)[prommodel.LabelName("__name__")])

		r := &catalog.Record{
			Origin:    c.name,
			Connector: c,
			Source:    string((*metric)[prommodel.LabelName(c.sourceLabel)]),
			Metric:    metricName + c.labelGlue + string(k) + c.labelGlue + string(v),
		}

		for _, p := range c.excludedLabels {
			if ok, err := pattern.Match(p, string(k)); err != nil {
				return nil, fmt.Errorf("error parsing excluded label: %s", err)
			} else if ok {
				c.log.Debug("discarding excluded label %q for metric %s", p, metricName)
				goto nextLabel
			}
		}

		records = append(records, r)

		c.metrics[r.Source+r.Metric] = &prometheusMetric{
			metric: metricName,
			label:  prommodel.LabelPair{Name: k, Value: v},
		}

	nextLabel:
	}

	return records, nil
}
