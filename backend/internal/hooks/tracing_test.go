package hooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// installTestProviders installs in-memory OTel tracer + meter providers so the
// test can inspect spans and metrics emitted by hook dispatch. Returns the
// span recorder + metric reader and restores the previous providers on t.Cleanup.
//
// Because hooksTracer / hooksMeter were resolved at init() time from the
// (then no-op) global providers, we also swap the package-level variables
// for the duration of the test. The approach matches what Go's own OTel
// contrib test helpers do.
func installTestProviders(t *testing.T) (*tracetest.SpanRecorder, *sdkmetric.ManualReader) {
	t.Helper()

	// Tracer side. Spans sample as AlwaysSample so the context propagates
	// (sampled=false skips traceparent emission for the outbound request).
	spanRec := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(spanRec),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	prevTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)

	// W3C TraceContext propagator so injectTraceContext populates the
	// Traceparent header on outbound requests. The production server
	// installs this globally at startup; tests must do the same.
	prevProp := otel.GetTextMapPropagator()
	otel.SetTextMapPropagator(propagation.TraceContext{})

	// Meter side
	mr := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(mr))
	prevMP := otel.GetMeterProvider()
	otel.SetMeterProvider(mp)

	// Swap the package-level tracer + meter so startDispatchSpan / startActionSpan
	// see the new providers. Also re-run init-time metric registration against
	// the new meter so the counters/histograms are wired.
	prevHooksTracer := hooksTracer
	prevHooksMeter := hooksMeter
	hooksTracer = tp.Tracer("hooks")
	hooksMeter = mp.Meter("hooks")
	prevMetrics := hmetrics

	var err error
	hmetrics.dispatchTotal, err = hooksMeter.Int64Counter("hook.dispatches_total")
	require.NoError(t, err)
	hmetrics.dispatchDuration, err = hooksMeter.Float64Histogram("hook.dispatch_duration")
	require.NoError(t, err)
	hmetrics.actionTotal, err = hooksMeter.Int64Counter("hook.action_invocations_total")
	require.NoError(t, err)
	hmetrics.actionDuration, err = hooksMeter.Float64Histogram("hook.action_invocation_duration")
	require.NoError(t, err)

	t.Cleanup(func() {
		otel.SetTracerProvider(prevTP)
		otel.SetMeterProvider(prevMP)
		otel.SetTextMapPropagator(prevProp)
		hooksTracer = prevHooksTracer
		hooksMeter = prevHooksMeter
		hmetrics = prevMetrics
	})
	return spanRec, mr
}

// collectMetric pulls the latest value for a named counter or histogram from
// a ManualReader, returning (data points found, total count).
func collectMetric(t *testing.T, mr *sdkmetric.ManualReader, name string) (dataPoints int, totalCount int64) {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, mr.Collect(context.Background(), &rm))
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != name {
				continue
			}
			switch d := m.Data.(type) {
			case metricdata.Sum[int64]:
				dataPoints = len(d.DataPoints)
				for _, dp := range d.DataPoints {
					totalCount += dp.Value
				}
			case metricdata.Histogram[float64]:
				dataPoints = len(d.DataPoints)
				for _, dp := range d.DataPoints {
					totalCount += int64(dp.Count)
				}
			}
		}
	}
	return
}

func TestDispatcher_EmitsSpanAndMetricsAndInjectsTraceparent(t *testing.T) {
	spanRec, mr := installTestProviders(t)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("Traceparent")
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: true})
	}))
	defer srv.Close()

	cfg := Config{Subscriptions: []Subscription{{
		Name:   "recorder",
		Events: []string{EventPreDeploy},
		URL:    srv.URL,
	}}}
	d, err := NewDispatcher(cfg, srv.Client())
	require.NoError(t, err)

	require.NoError(t, d.Fire(context.Background(), EventPreDeploy, EventEnvelope{}))

	// ---- span assertions ----
	spans := spanRec.Ended()
	require.Len(t, spans, 1, "exactly one hooks.dispatch span")
	sp := spans[0]
	assert.Equal(t, "hooks.dispatch", sp.Name())
	attrs := map[string]string{}
	for _, kv := range sp.Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, EventPreDeploy, attrs[attrEvent])
	assert.Equal(t, "recorder", attrs[attrSubscription])
	assert.Equal(t, outcomeSuccess, attrs[attrOutcome])
	assert.Equal(t, "200", attrs[attrStatusCode])

	// ---- traceparent assertions ----
	require.NotEmpty(t, gotTraceparent, "subscriber must receive Traceparent header")
	assert.True(t, strings.HasPrefix(gotTraceparent, "00-"),
		"traceparent version must be 00 (W3C spec); got %q", gotTraceparent)
	// traceparent format: 00-<trace-id 32 hex>-<span-id 16 hex>-<flags 2 hex>
	assert.Len(t, gotTraceparent, 55, "traceparent wrong length: %q", gotTraceparent)

	// ---- metric assertions ----
	dp, total := collectMetric(t, mr, "hook.dispatches_total")
	assert.Equal(t, 1, dp, "one data-point label set (event/sub/outcome)")
	assert.Equal(t, int64(1), total)
	dp, _ = collectMetric(t, mr, "hook.dispatch_duration")
	assert.Equal(t, 1, dp, "histogram recorded one measurement")
}

func TestDispatcher_DeniedOutcomeRecorded(t *testing.T) {
	spanRec, mr := installTestProviders(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(HookResponse{Allowed: false, Message: "policy says no"})
	}))
	defer srv.Close()

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name:          "deny",
		Events:        []string{EventPreDeploy},
		URL:           srv.URL,
		FailurePolicy: FailurePolicyFail,
	}}}, srv.Client())
	require.NoError(t, err)

	err = d.Fire(context.Background(), EventPreDeploy, EventEnvelope{})
	require.Error(t, err)

	spans := spanRec.Ended()
	require.Len(t, spans, 1)
	attrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, outcomeDenied, attrs[attrOutcome],
		"denied subscriber must produce outcome=denied (not http_error)")

	_, total := collectMetric(t, mr, "hook.dispatches_total")
	assert.Equal(t, int64(1), total)
}

func TestDispatcher_TransportErrorOutcomeRecorded(t *testing.T) {
	spanRec, mr := installTestProviders(t)

	d, err := NewDispatcher(Config{Subscriptions: []Subscription{{
		Name:          "dead",
		Events:        []string{EventPreDeploy},
		URL:           "http://127.0.0.1:0/", // guaranteed unreachable
		FailurePolicy: FailurePolicyFail,
	}}}, http.DefaultClient)
	require.NoError(t, err)

	err = d.Fire(context.Background(), EventPreDeploy, EventEnvelope{})
	require.Error(t, err)

	spans := spanRec.Ended()
	require.Len(t, spans, 1)
	var outcome string
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == attrOutcome {
			outcome = kv.Value.Emit()
		}
	}
	assert.Contains(t, []string{outcomeTransportError, outcomeTimeout}, outcome,
		"unreachable host should be transport_error or timeout, got %q", outcome)

	_, total := collectMetric(t, mr, "hook.dispatches_total")
	assert.Equal(t, int64(1), total)
}

func TestActionRegistry_EmitsSpanAndMetricsAndInjectsTraceparent(t *testing.T) {
	spanRec, mr := installTestProviders(t)

	var gotTraceparent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTraceparent = r.Header.Get("Traceparent")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer srv.Close()

	reg, err := NewActionRegistry([]ActionSubscription{{
		Name:           "x",
		URL:            srv.URL,
		TimeoutSeconds: 5,
	}}, srv.Client())
	require.NoError(t, err)

	_, err = reg.Invoke(context.Background(), "x", &InstanceRef{ID: "i-1"}, nil)
	require.NoError(t, err)

	spans := spanRec.Ended()
	require.Len(t, spans, 1)
	assert.Equal(t, "hooks.action", spans[0].Name())
	attrs := map[string]string{}
	for _, kv := range spans[0].Attributes() {
		attrs[string(kv.Key)] = kv.Value.Emit()
	}
	assert.Equal(t, "x", attrs[attrAction])
	assert.Equal(t, outcomeSuccess, attrs[attrOutcome])
	assert.Equal(t, "200", attrs[attrStatusCode])

	require.NotEmpty(t, gotTraceparent, "action subscriber must receive Traceparent")
	assert.True(t, strings.HasPrefix(gotTraceparent, "00-"))

	_, total := collectMetric(t, mr, "hook.action_invocations_total")
	assert.Equal(t, int64(1), total)
}

func TestActionRegistry_UnknownActionIsRecorded(t *testing.T) {
	spanRec, mr := installTestProviders(t)

	reg, err := NewActionRegistry(nil, nil)
	require.NoError(t, err)

	_, err = reg.Invoke(context.Background(), "missing", nil, nil)
	require.Error(t, err)

	spans := spanRec.Ended()
	require.Len(t, spans, 1)
	var outcome string
	for _, kv := range spans[0].Attributes() {
		if string(kv.Key) == attrOutcome {
			outcome = kv.Value.Emit()
		}
	}
	assert.Equal(t, outcomeUnknownAction, outcome)

	_, total := collectMetric(t, mr, "hook.action_invocations_total")
	assert.Equal(t, int64(1), total, "unknown-action must still produce one metric data-point")
}

func TestClassifyErr(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"":                              outcomeSuccess, // nil path: caller handles
		"hook returned status 500: ...": outcomeHTTPError,
		"action returned status 502":    outcomeHTTPError,
		"context deadline exceeded":     outcomeTimeout,
		"Client.Timeout exceeded while awaiting headers": outcomeTimeout,
		"dial tcp: connection refused":                   outcomeTransportError,
		"no such host":                                   outcomeTransportError,
	}
	for msg, want := range tests {
		msg, want := msg, want
		t.Run(want+"/"+truncate(msg, 30), func(t *testing.T) {
			t.Parallel()
			var e error
			if msg != "" {
				e = stringErr(msg)
			}
			assert.Equal(t, want, classifyErr(e))
		})
	}
}

type stringErr string

func (s stringErr) Error() string { return string(s) }
