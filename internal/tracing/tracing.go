// Пакет tracing поднимает OpenTelemetry-трассировку сервера (итерация 34):
// глобальный TracerProvider с OTLP/stdout-экспортёром, W3C-пропагатор и функцию
// slив (flush+shutdown). Инструментируется только control-plane (HTTP-API,
// рукопожатие join, SQL-запросы) — игровой тик 30 Гц НЕ трассируется (zero-alloc
// горячего пути неприкосновенен).
//
// Выключен по умолчанию: без экспортёра остаётся глобальный no-op провайдер, поэтому
// `otel.Tracer(...).Start(...)` в коде — почти бесплатная операция, а инструментовку
// можно ставить безусловно (без ветвлений по флагу).
package tracing

import (
	"context"
	"fmt"
	"strings"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Config конфигурирует трассировку. Enabled=false и Stdout=false — трассировка
// выключена (no-op провайдер, нулевые накладные).
type Config struct {
	Enabled     bool    // включить OTLP-экспорт трейсов
	Endpoint    string  // OTLP endpoint: "host:4318" или полный URL; пусто — из OTEL_EXPORTER_OTLP_ENDPOINT/дефолт
	Insecure    bool    // OTLP без TLS (локальный коллектор)
	Stdout      bool    // dev: экспорт трейсов в stdout (можно вместе с OTLP)
	ServiceName string  // имя сервиса в трейсах (пусто — "arena-server")
	Version     string  // версия сборки (атрибут service.version)
	SampleRatio float64 // доля семплируемых корневых трейсов (<=0 → 1.0, всё)
}

func (c Config) active() bool { return c.Enabled || c.Stdout }

// Setup ставит глобальный пропагатор и, если трассировка включена, глобальный
// TracerProvider с батч-экспортом. Возвращает функцию слива (flush+shutdown),
// которую надо вызвать при завершении сервера, чтобы дослать буферизованные спаны.
// При выключенной трассировке возвращает no-op слив.
func Setup(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	// Пропагатор ставим всегда — он безвреден и позволяет подхватывать входящий
	// traceparent, даже пока экспорт выключен.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if !cfg.active() {
		return func(context.Context) error { return nil }, nil
	}

	var exporters []sdktrace.SpanExporter
	if cfg.Enabled {
		exp, err := otlptracehttp.New(ctx, otlpOptions(cfg)...)
		if err != nil {
			return nil, fmt.Errorf("tracing: otlp exporter: %w", err)
		}
		exporters = append(exporters, exp)
	}
	if cfg.Stdout {
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("tracing: stdout exporter: %w", err)
		}
		exporters = append(exporters, exp)
	}

	tp := newProvider(resourceFor(cfg), samplerFor(cfg), exporters...)
	otel.SetTracerProvider(tp)
	return tp.Shutdown, nil
}

// newProvider собирает TracerProvider из ресурса, семплера и набора экспортёров
// (каждый — через батч-процессор). Вынесено отдельно для тестов с in-memory
// экспортёром.
func newProvider(res *resource.Resource, sampler sdktrace.Sampler, exporters ...sdktrace.SpanExporter) *sdktrace.TracerProvider {
	opts := []sdktrace.TracerProviderOption{
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sampler),
	}
	for _, e := range exporters {
		opts = append(opts, sdktrace.WithBatcher(e))
	}
	return sdktrace.NewTracerProvider(opts...)
}

func samplerFor(cfg Config) sdktrace.Sampler {
	ratio := cfg.SampleRatio
	if ratio <= 0 {
		ratio = 1
	}
	// ParentBased: у дочерних решений слушаем родителя, корни — по доле ratio.
	return sdktrace.ParentBased(sdktrace.TraceIDRatioBased(ratio))
}

// resourceFor описывает сервис в трейсах. NewSchemaless — без schema URL, чтобы не
// ловить конфликт версий semconv при слиянии ресурсов.
func resourceFor(cfg Config) *resource.Resource {
	name := cfg.ServiceName
	if name == "" {
		name = "arena-server"
	}
	attrs := []attribute.KeyValue{attribute.String("service.name", name)}
	if cfg.Version != "" {
		attrs = append(attrs, attribute.String("service.version", cfg.Version))
	}
	return resource.NewSchemaless(attrs...)
}

// otlpOptions собирает опции OTLP/HTTP-экспортёра. Полный URL (со схемой) идёт как
// WithEndpointURL; голый host:port — как WithEndpoint (+WithInsecure по флагу). Пустой
// endpoint оставляет экспортёру самонастройку из OTEL_EXPORTER_OTLP_ENDPOINT/дефолта.
func otlpOptions(cfg Config) []otlptracehttp.Option {
	var opts []otlptracehttp.Option
	if cfg.Endpoint != "" {
		if strings.Contains(cfg.Endpoint, "://") {
			opts = append(opts, otlptracehttp.WithEndpointURL(cfg.Endpoint))
		} else {
			opts = append(opts, otlptracehttp.WithEndpoint(cfg.Endpoint))
		}
	}
	if cfg.Insecure {
		opts = append(opts, otlptracehttp.WithInsecure())
	}
	return opts
}
