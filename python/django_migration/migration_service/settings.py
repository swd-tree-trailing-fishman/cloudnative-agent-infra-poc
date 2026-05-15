import os
from pathlib import Path

BASE_DIR = Path(__file__).resolve().parent.parent
SECRET_KEY = os.environ.get("DJANGO_SECRET_KEY", "dev-secret-key-change-in-prod")
DEBUG = os.environ.get("DEBUG", "false").lower() == "true"

ALLOWED_HOSTS = os.environ.get("ALLOWED_HOSTS", "*").split(",")

INSTALLED_APPS = [
    "django.contrib.contenttypes",
    "django.contrib.auth",
    "rest_framework",
    "migration_service",
]

MIDDLEWARE = [
    "django.middleware.security.SecurityMiddleware",
    "django.middleware.common.CommonMiddleware",
]

ROOT_URLCONF = "migration_service.urls"

DATABASES = {
    "default": {
        "ENGINE": "django.db.backends.sqlite3",
        "NAME": BASE_DIR / "db.sqlite3",
    }
}

DEFAULT_AUTO_FIELD = "django.db.models.BigAutoField"

# OpenTelemetry
OTEL_ENDPOINT = os.environ.get("OTEL_EXPORTER_OTLP_ENDPOINT", "localhost:4317")
OTEL_SERVICE_NAME = "django-migration-service"

# AWS / LocalStack
AWS_ENDPOINT_URL = os.environ.get("AWS_ENDPOINT_URL", "http://localhost:4566")
AWS_DEFAULT_REGION = os.environ.get("AWS_DEFAULT_REGION", "ap-northeast-1")
SQS_QUEUE_URL = os.environ.get(
    "SQS_QUEUE_URL",
    "http://localhost:4566/000000000000/db-migration-queue",
)
