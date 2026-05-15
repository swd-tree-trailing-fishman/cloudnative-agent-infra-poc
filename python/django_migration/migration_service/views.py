"""
Django Migration Service Views

Pythonでの学習目的:
  - Django DRF を使った REST API
  - boto3 で SQS (LocalStack) にメッセージを送信
  - OpenTelemetry でトレース計装
"""
import json
import time
import uuid
import logging

import boto3
from botocore.exceptions import ClientError
from django.conf import settings
from django.http import JsonResponse
from django.views.decorators.csrf import csrf_exempt
from django.views.decorators.http import require_http_methods
from opentelemetry import trace
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import BatchSpanProcessor
from opentelemetry.exporter.otlp.proto.grpc.trace_exporter import OTLPSpanExporter
from opentelemetry.sdk.resources import Resource

logger = logging.getLogger(__name__)

# ---------------------------------------------------------------------------
# OpenTelemetry セットアップ (モジュールロード時に一度だけ初期化)
# ---------------------------------------------------------------------------
_tracer = None


def _get_tracer() -> trace.Tracer:
    global _tracer
    if _tracer is not None:
        return _tracer

    resource = Resource.create({"service.name": settings.OTEL_SERVICE_NAME})
    provider = TracerProvider(resource=resource)
    try:
        exporter = OTLPSpanExporter(
            endpoint=settings.OTEL_ENDPOINT,
            insecure=True,
        )
        provider.add_span_processor(BatchSpanProcessor(exporter))
    except Exception as exc:  # noqa: BLE001
        logger.warning("OTLP exporter init failed (tracing disabled): %s", exc)

    trace.set_tracer_provider(provider)
    _tracer = trace.get_tracer(settings.OTEL_SERVICE_NAME)
    return _tracer


# ---------------------------------------------------------------------------
# SQSクライアント (LocalStack)
# ---------------------------------------------------------------------------
def _get_sqs_client():
    return boto3.client(
        "sqs",
        region_name=settings.AWS_DEFAULT_REGION,
        endpoint_url=settings.AWS_ENDPOINT_URL,
        aws_access_key_id="test",
        aws_secret_access_key="test",
    )


# ---------------------------------------------------------------------------
# Views
# ---------------------------------------------------------------------------

def health_check(request):
    """ヘルスチェックエンドポイント (K8s readinessProbe 用)"""
    return JsonResponse({"status": "ok", "service": settings.OTEL_SERVICE_NAME})


@csrf_exempt
@require_http_methods(["POST"])
def trigger_migration(request):
    """
    マイグレーションを直接トリガーするエンドポイント。
    実際の処理はダミーだが、OTELスパンを発行してJaegerで可視化できる。
    """
    tracer = _get_tracer()
    with tracer.start_as_current_span("migration.trigger") as span:
        try:
            body = json.loads(request.body or "{}")
        except json.JSONDecodeError:
            return JsonResponse({"error": "invalid JSON"}, status=400)

        migration_id = body.get("migration_id", str(uuid.uuid4()))
        target_db = body.get("db", "default")

        span.set_attribute("migration.id", migration_id)
        span.set_attribute("migration.target_db", target_db)

        # --- ダミーマイグレーション処理 ---
        steps = _run_dummy_migration(tracer, migration_id, target_db)

        return JsonResponse({
            "migration_id": migration_id,
            "target_db": target_db,
            "steps_completed": steps,
            "status": "completed",
            "trace_id": format(span.get_span_context().trace_id, "032x"),
        })


def _run_dummy_migration(tracer, migration_id: str, target_db: str) -> list:
    """ダミーのマイグレーションステップをトレース付きで実行"""
    migrations = [
        "001_create_users_table",
        "002_add_agent_logs",
        "003_add_feature_flags",
    ]
    completed = []
    for migration in migrations:
        with tracer.start_as_current_span(f"migration.step.{migration}") as step_span:
            step_span.set_attribute("migration.step", migration)
            step_span.set_attribute("migration.db", target_db)
            time.sleep(0.1)  # ダミー処理
            completed.append(migration)
            logger.info("[MIGRATION] Applied: %s on %s", migration, target_db)

    return completed


@csrf_exempt
@require_http_methods(["POST"])
def send_sqs_event(request):
    """
    SQS (LocalStack) にマイグレーションイベントを送信する。
    KEDA ScaledJob のトリガーとして使用。
    """
    tracer = _get_tracer()
    with tracer.start_as_current_span("sqs.send_migration_event") as span:
        try:
            body = json.loads(request.body or "{}")
        except json.JSONDecodeError:
            return JsonResponse({"error": "invalid JSON"}, status=400)

        migration_id = body.get("migration_id", str(uuid.uuid4()))
        payload = {
            "migration_id": migration_id,
            "db": body.get("db", "default"),
            "version": body.get("version", "latest"),
            "requested_at": time.time(),
        }

        span.set_attribute("sqs.queue_url", settings.SQS_QUEUE_URL)
        span.set_attribute("migration.id", migration_id)

        try:
            sqs = _get_sqs_client()
            response = sqs.send_message(
                QueueUrl=settings.SQS_QUEUE_URL,
                MessageBody=json.dumps(payload),
                MessageGroupId="migration",  # FIFO対応 (標準キューでは無視される)
            )
            message_id = response.get("MessageId", "unknown")
            span.set_attribute("sqs.message_id", message_id)

            return JsonResponse({
                "message_id": message_id,
                "migration_id": migration_id,
                "status": "sent",
                "trace_id": format(span.get_span_context().trace_id, "032x"),
            })

        except ClientError as exc:
            logger.error("SQS send failed: %s", exc)
            span.record_exception(exc)
            return JsonResponse({"error": str(exc)}, status=500)


def migration_status(request):
    """SQSキューのメッセージ数を返す (KEDAトリガー確認用)"""
    tracer = _get_tracer()
    with tracer.start_as_current_span("sqs.get_queue_attributes") as span:
        try:
            sqs = _get_sqs_client()
            attrs = sqs.get_queue_attributes(
                QueueUrl=settings.SQS_QUEUE_URL,
                AttributeNames=["ApproximateNumberOfMessages"],
            )
            pending = attrs["Attributes"].get("ApproximateNumberOfMessages", "0")
            span.set_attribute("sqs.pending_messages", pending)
            return JsonResponse({
                "queue_url": settings.SQS_QUEUE_URL,
                "pending_messages": int(pending),
                "status": "ok",
            })
        except ClientError as exc:
            logger.warning("SQS status check failed: %s", exc)
            return JsonResponse({
                "queue_url": settings.SQS_QUEUE_URL,
                "pending_messages": -1,
                "status": "error",
                "error": str(exc),
            }, status=503)
