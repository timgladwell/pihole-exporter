package exporter

import (
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/timgladwell/pihole-exporter/internal/piholetest"
	"github.com/timgladwell/pihole-exporter/pkg/pihole"

	dto "github.com/prometheus/client_model/go"
)

const password = "app-password"

func newMockedCollector(t *testing.T) (*Collector, *piholetest.Mock) {
	t.Helper()

	mock := piholetest.New(t)
	client, err := pihole.NewAuthClient(piholetest.BaseURL, password, pihole.WithHTTPClient(mock.Client()))
	if err != nil {
		t.Fatalf("NewAuthClient() error = %v", err)
	}
	return NewCollector(client, 5*time.Second), mock
}

// expectScrapes sets up a login followed by the given number of full scrapes.
func expectScrapes(mock *piholetest.Mock, scrapes int) {
	login := mock.ExpectLogin(password)
	mock.ExpectScrape(login, "sid-1", "csrf-1", scrapes)
}

func gather(t *testing.T, collector *Collector) []*dto.MetricFamily {
	t.Helper()

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	return families
}

func TestCollectorEmitsScrapeHealthMetrics(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	expectScrapes(mock, 1)

	families := gather(t, collector)

	if got := metricValue(t, families, "pihole_exporter_scrape_success"); got != 1 {
		t.Fatalf("pihole_exporter_scrape_success = %v, want 1", got)
	}
	if got := metricValue(t, families, "pihole_exporter_scrape_duration_seconds"); got < 0 {
		t.Fatalf("pihole_exporter_scrape_duration_seconds = %v, want >= 0", got)
	}
}

// TestCollectorReportsPiholeValuesUnchanged is the end-to-end payload check:
// every value in the fixtures reaches the scrape under the right name and
// labels. The expected values are read off the fixture files by hand, not
// computed, so a bug in path lookup, label expansion or number conversion
// shows up here.
func TestCollectorReportsPiholeValuesUnchanged(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	expectScrapes(mock, 1)

	got := piholeSeries(gather(t, collector))

	want := map[string]float64{
		`pihole_query_types_by_type{type="A"}`:                 4101,
		`pihole_query_types_by_type{type="AAAA"}`:              2213,
		`pihole_query_types_by_type{type="HTTPS"}`:             1183,
		`pihole_summary_clients_active`:                        12,
		`pihole_summary_clients_total`:                         19,
		`pihole_summary_gravity_domains_being_blocked`:         104523,
		`pihole_summary_gravity_last_update_timestamp_seconds`: 1727800000,
		`pihole_summary_queries_blocked`:                       3465,
		`pihole_summary_queries_by_reply{reply="IP"}`:          3912,
		`pihole_summary_queries_by_reply{reply="NODATA"}`:      0,
		`pihole_summary_queries_by_reply{reply="NXDOMAIN"}`:    120,
		`pihole_summary_queries_by_status{status="CACHE"}`:     26,
		`pihole_summary_queries_by_status{status="FORWARDED"}`: 4006,
		`pihole_summary_queries_by_status{status="GRAVITY"}`:   3465,
		`pihole_summary_queries_by_type{type="A"}`:             4101,
		`pihole_summary_queries_by_type{type="AAAA"}`:          2213,
		`pihole_summary_queries_by_type{type="HTTPS"}`:         1183,
		`pihole_summary_queries_cached`:                        26,
		`pihole_summary_queries_forwarded`:                     4006,
		`pihole_summary_queries_frequency`:                     0,
		`pihole_summary_queries_percent_blocked`:               46.218487,
		`pihole_summary_queries_total`:                         7497,
		`pihole_summary_queries_unique_domains`:                445,
		`pihole_top_clients_blocked_queries`:                   3465,
		`pihole_top_clients_total_queries`:                     7497,
		`pihole_top_domains_blocked_queries`:                   3465,
		`pihole_top_domains_total_queries`:                     7497,
		`pihole_upstreams_forwarded_queries`:                   4006,
		`pihole_upstreams_total_queries`:                       7497,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("Pi-hole series mismatch (-want +got):\n%s", diff)
	}
}

func TestCollectorEmitsPiholeMetricsAsGauges(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	expectScrapes(mock, 1)

	for _, family := range gather(t, collector) {
		if strings.HasPrefix(family.GetName(), "pihole_exporter_") {
			continue
		}
		if family.GetType() != dto.MetricType_GAUGE {
			t.Errorf("%s type = %v, want GAUGE", family.GetName(), family.GetType())
		}
	}
}

// TestCollectorReusesTheSessionAcrossScrapes: one login, two scrapes. A
// second login would match no expectation and fail the test.
func TestCollectorReusesTheSessionAcrossScrapes(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	expectScrapes(mock, 2)

	gather(t, collector)
	gather(t, collector)
}

// TestCollectorRequestsNoStatsWhenAuthenticationFails: the mock expects only
// the login, so any stats request after the 500 fails the test.
func TestCollectorRequestsNoStatsWhenAuthenticationFails(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	mock.ExpectLogin(password).Reply(http.StatusInternalServerError, nil)

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	_, _ = registry.Gather()
}

func TestCollectorNotReadyAfterFailedScrape(t *testing.T) {
	t.Parallel()
	collector, mock := newMockedCollector(t)
	mock.ExpectLogin(password).Reply(http.StatusInternalServerError, nil)

	if collector.Ready() {
		t.Fatal("Ready() = true before any scrape, want false")
	}

	registry := prometheus.NewRegistry()
	registry.MustRegister(collector)
	if _, err := registry.Gather(); err == nil {
		t.Fatal("Gather() error = nil, want scrape error")
	}

	if collector.Ready() {
		t.Fatal("Ready() = true after failed scrape, want false")
	}
}

func metricValue(t *testing.T, families []*dto.MetricFamily, name string) float64 {
	t.Helper()

	for _, family := range families {
		if family.GetName() != name {
			continue
		}
		metrics := family.GetMetric()
		if len(metrics) != 1 {
			t.Fatalf("%s has %d metrics, want 1", name, len(metrics))
		}
		return metrics[0].GetGauge().GetValue()
	}

	t.Fatalf("metric %s not found", name)
	return 0
}

// piholeSeries flattens the Pi-hole metrics (not the exporter's own) to
// `name{label="value"}` -> value, the way they read in the exposition format.
func piholeSeries(families []*dto.MetricFamily) map[string]float64 {
	series := make(map[string]float64)
	for _, family := range families {
		if strings.HasPrefix(family.GetName(), "pihole_exporter_") {
			continue
		}
		for _, metric := range family.GetMetric() {
			var labels []string
			for _, pair := range metric.GetLabel() {
				labels = append(labels, pair.GetName()+`="`+pair.GetValue()+`"`)
			}
			sort.Strings(labels)
			key := family.GetName()
			if len(labels) > 0 {
				key += "{" + strings.Join(labels, ",") + "}"
			}
			series[key] = metric.GetGauge().GetValue()
		}
	}
	return series
}
