module github.com/icomm-api/bizgo-sdk-comm-go/otel

go 1.26.0

require (
	github.com/icomm-api/bizgo-sdk-comm-go v1.2.0
	go.opentelemetry.io/otel v1.46.0
	go.opentelemetry.io/otel/sdk v1.46.0
	go.opentelemetry.io/otel/trace v1.46.0
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/go-logr/logr v1.4.4 // indirect
	github.com/go-logr/stdr v1.2.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	go.opentelemetry.io/auto/sdk v1.2.1 // indirect
	go.opentelemetry.io/otel/metric v1.46.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)

// The adapter is developed together with the SDK and requires the SDK of the same version.
// The replace directive makes local builds and tests use ../ (also before the SDK tag exists); it is
// ignored by modules that depend on this one, which get the tagged SDK from the module proxy.
// The otel/v* release job drops it and tests against the published SDK.
replace github.com/icomm-api/bizgo-sdk-comm-go => ../
