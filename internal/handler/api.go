package handler

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/cloudnative-poc/agent-infra/internal/feature"
	"github.com/cloudnative-poc/agent-infra/internal/sandbox"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

type API struct {
	toggle  *feature.Toggle
	sandbox *sandbox.Runner
}

func New(toggle *feature.Toggle, sandboxRunner *sandbox.Runner) *API {
	return &API{toggle: toggle, sandbox: sandboxRunner}
}

func (a *API) RegisterRoutes(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", a.handleHealth)
	mux.HandleFunc("/api/recommend", a.handleRecommend)
	mux.HandleFunc("/api/execute", a.handleExecute)
}

// handleHealth is a simple liveness probe endpoint.
func (a *API) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// handleRecommend demonstrates Feature Toggle + OTEL tracing.
func (a *API) handleRecommend(w http.ResponseWriter, r *http.Request) {
	tracer := otel.Tracer("agent-infra/handler")
	ctx, span := tracer.Start(r.Context(), "recommend.handle",
		trace.WithAttributes(
			attribute.String("http.method", r.Method),
			attribute.String("http.path", r.URL.Path),
		),
	)
	defer span.End()

	region := r.Header.Get("X-Region")
	if region == "" {
		region = r.URL.Query().Get("region")
	}
	cfg := a.toggle.ConfigForRegion(region)

	span.SetAttributes(
		attribute.String("feature.region", string(cfg.Region)),
		attribute.Bool("feature.new_engine", cfg.EnableNewRecommendEngine),
		attribute.String("feature.lang", cfg.DefaultLang),
	)

	result := a.buildRecommendation(ctx, cfg)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func (a *API) buildRecommendation(ctx context.Context, cfg feature.Config) map[string]interface{} {
	tracer := otel.Tracer("agent-infra/handler")
	_, span := tracer.Start(ctx, "recommend.llm_call")
	defer span.End()

	// Simulate LLM processing latency
	time.Sleep(50 * time.Millisecond)

	span.SetAttributes(attribute.Bool("llm.mock", true))

	items := []string{"item-A", "item-B", "item-C"}
	if cfg.EnableNewRecommendEngine {
		items = append(items, "item-D-new")
	}

	msgs := map[string]string{
		"en":    "Here are your recommendations",
		"ja":    "おすすめアイテムです",
		"zh-TW": "以下是推薦項目",
		"ko":    "추천 항목입니다",
	}
	msg := msgs[cfg.DefaultLang]
	if msg == "" {
		msg = msgs["en"]
	}

	return map[string]interface{}{
		"region":      cfg.Region,
		"lang":        cfg.DefaultLang,
		"new_engine":  cfg.EnableNewRecommendEngine,
		"message":     msg,
		"items":       items,
		"trace_id":    span.SpanContext().TraceID().String(),
	}
}

// handleExecute spawns a sandbox K8s Job for agent code execution requests.
func (a *API) handleExecute(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	tracer := otel.Tracer("agent-infra/handler")
	ctx, span := tracer.Start(r.Context(), "sandbox.execute")
	defer span.End()

	var req struct {
		Command string `json:"command"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	if a.sandbox == nil {
		// Sandbox unavailable (e.g., no K8s in local dev) — return mock response
		span.SetAttributes(attribute.Bool("sandbox.mock", true))
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"job_name": "mock-job-0000",
			"status":   "mock_created",
			"message":  "[MOCK] Sandbox job would be created here",
			"trace_id": span.SpanContext().TraceID().String(),
		})
		return
	}

	result, err := a.sandbox.Execute(ctx, sandbox.ExecutionRequest{Command: req.Command})
	if err != nil {
		log.Printf("sandbox execute error: %v", err)
		span.SetStatus(codes.Error, err.Error())
		http.Error(w, "failed to create sandbox job", http.StatusInternalServerError)
		return
	}

	span.SetAttributes(attribute.String("sandbox.job_name", result.JobName))

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"job_name":   result.JobName,
		"status":     result.Status,
		"message":    result.Message,
		"started_at": result.StartedAt,
		"trace_id":   span.SpanContext().TraceID().String(),
	})
}
