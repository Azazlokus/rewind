package tracing

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// TestSetupDisabled: без экспортёров Setup ставит пропагатор, но не трогает
// глобальный провайдер и возвращает работающий no-op слив.
func TestSetupDisabled(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{})
	if err != nil {
		t.Fatalf("Setup(disabled): %v", err)
	}
	if shutdown == nil {
		t.Fatal("shutdown must be non-nil even when disabled")
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("no-op shutdown should not error: %v", err)
	}
	// Пропагатор должен быть установлен (умеет извлекать/вставлять traceparent).
	if fields := otel.GetTextMapPropagator().Fields(); len(fields) == 0 {
		t.Fatal("expected a text-map propagator to be installed")
	}
}

// TestSetupStdoutInstallsProvider: со stdout-экспортёром Setup ставит настоящий
// SDK-провайдер, а слив завершается без ошибки.
func TestSetupStdoutInstallsProvider(t *testing.T) {
	shutdown, err := Setup(context.Background(), Config{Stdout: true, ServiceName: "test-svc"})
	if err != nil {
		t.Fatalf("Setup(stdout): %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	if _, ok := otel.GetTracerProvider().(interface{ Shutdown(context.Context) error }); !ok {
		t.Fatal("expected a real (shutdownable) TracerProvider to be installed")
	}
}

// TestProviderExportsSpan: провайдер, собранный newProvider с in-memory экспортёром,
// действительно доставляет завершённый спан после ForceFlush — проверка конвейера
// (ресурс/семплер/батч-процессор/экспортёр).
func TestProviderExportsSpan(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp := newProvider(resourceFor(Config{ServiceName: "test-svc"}), samplerFor(Config{}), exp)
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })

	_, span := tp.Tracer("test").Start(context.Background(), "unit.span")
	span.End()
	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("force flush: %v", err)
	}

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("expected exactly one exported span, got %d", len(spans))
	}
	if spans[0].Name != "unit.span" {
		t.Fatalf("exported span name = %q, want unit.span", spans[0].Name)
	}
}
