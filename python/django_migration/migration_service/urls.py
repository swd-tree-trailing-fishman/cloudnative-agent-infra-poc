from django.urls import path
from . import views

urlpatterns = [
    path("healthz", views.health_check),
    path("api/migration/trigger", views.trigger_migration),
    path("api/migration/status", views.migration_status),
    path("api/migration/send-event", views.send_sqs_event),
]
