SHELL := /bin/bash

# =============================================================================
# Cloud Native AI Agent Infra - PoC Makefile
# 前提: WSL上でkind, kubectl, helm, docker, curl がインストール済み
# =============================================================================

CLUSTER_NAME   := agent-poc
KIND_CONFIG    := kind-config.yaml
APP_IMAGE      := agent-backend:latest
DJANGO_IMAGE   := django-migration:latest
NAMESPACE_APP  := agent-infra
NAMESPACE_OBS  := observability
NAMESPACE_EVT  := event-driven
NAMESPACE_SBX  := agent-sandbox

# Helm チャートバージョン
ISTIO_VERSION  := 1.21.1
KEDA_VERSION   := 2.14.0

.PHONY: all cluster-up istio-install keda-install deploy deploy-python \
        test-request test-recommend test-execute test-migration \
        send-migration-event port-forward-jaeger port-forward-app \
        logs-backend logs-migration cluster-down clean docs-serve help

# デフォルトターゲット
all: cluster-up deploy

# =============================================================================
# クラスタセットアップ
# =============================================================================

## Kind クラスタを作成し、Istio/KEDA/OTLP 等を一括インストール
cluster-up: kind-config-gen kind-create istio-install keda-install
	@echo "✅ Cluster and base tools are ready."
	@echo "   Run 'make deploy' to deploy the application."

kind-config-gen:
	@cat > $(KIND_CONFIG) <<'EOF'
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
name: $(CLUSTER_NAME)
nodes:
  - role: control-plane
    kubeadmConfigPatches:
      - |
        kind: InitConfiguration
        nodeRegistration:
          kubeletExtraArgs:
            node-labels: "ingress-ready=true"
    extraPortMappings:
      - containerPort: 30686
        hostPort: 16686
        protocol: TCP
      - containerPort: 30080
        hostPort: 8080
        protocol: TCP
  - role: worker
  - role: worker
EOF

kind-create: kind-config-gen
	@if kind get clusters | grep -q "^$(CLUSTER_NAME)$$"; then \
		echo "Cluster $(CLUSTER_NAME) already exists, skipping creation."; \
	else \
		kind create cluster --config $(KIND_CONFIG) --name $(CLUSTER_NAME); \
	fi
	@kubectl cluster-info --context kind-$(CLUSTER_NAME)

## Istio Ambient Mesh インストール
istio-install:
	@echo ">>> Installing Istio $(ISTIO_VERSION) (Ambient mode)..."
	@if ! command -v istioctl &>/dev/null; then \
		curl -L https://istio.io/downloadIstio | ISTIO_VERSION=$(ISTIO_VERSION) TARGET_ARCH=x86_64 sh - && \
		export PATH="$$PWD/istio-$(ISTIO_VERSION)/bin:$$PATH"; \
	fi
	istioctl install --set profile=ambient --set meshConfig.accessLogFile=/dev/stdout -y
	kubectl apply -f https://github.com/kubernetes-sigs/gateway-api/releases/download/v1.1.0/standard-install.yaml
	@echo "✅ Istio Ambient installed."

## KEDA インストール
keda-install:
	@echo ">>> Installing KEDA $(KEDA_VERSION)..."
	helm repo add kedacore https://kedacore.github.io/charts 2>/dev/null || true
	helm repo update
	helm upgrade --install keda kedacore/keda \
		--namespace keda \
		--create-namespace \
		--version $(KEDA_VERSION) \
		--wait
	@echo "✅ KEDA installed."

# =============================================================================
# アプリケーションデプロイ
# =============================================================================

## Go バックエンドと Django サービスをビルド → Kind にロード → K8s にデプロイ
deploy: build-go load-go-image deploy-k8s deploy-python
	@echo "✅ All services deployed."
	@echo "   Run 'make port-forward-app' to access the API."

build-go:
	@echo ">>> Building Go backend..."
	docker build -t $(APP_IMAGE) .

load-go-image:
	@echo ">>> Loading image into Kind..."
	kind load docker-image $(APP_IMAGE) --name $(CLUSTER_NAME)

deploy-k8s:
	@echo ">>> Applying K8s manifests..."
	kubectl apply -f k8s/observability.yaml
	kubectl apply -f k8s/app.yaml
	kubectl apply -f k8s/istio-ambient.yaml
	kubectl apply -f k8s/migration-scaledjob.yaml
	@echo ">>> Waiting for deployments to be ready..."
	kubectl rollout status deployment/otel-collector -n $(NAMESPACE_OBS) --timeout=120s || true
	kubectl rollout status deployment/jaeger         -n $(NAMESPACE_OBS) --timeout=120s || true
	kubectl rollout status deployment/agent-backend  -n $(NAMESPACE_APP)  --timeout=120s || true
	kubectl rollout status deployment/localstack     -n $(NAMESPACE_EVT)  --timeout=120s || true

deploy-python: build-python load-python-image
	@echo ">>> Deploying Django migration service..."
	kubectl apply -f k8s/django-migration.yaml 2>/dev/null || \
		kubectl -n $(NAMESPACE_EVT) run django-migration \
			--image=$(DJANGO_IMAGE) \
			--image-pull-policy=Never \
			--port=8000 \
			--env="AWS_ENDPOINT_URL=http://localstack.$(NAMESPACE_EVT).svc.cluster.local:4566" \
			--env="SQS_QUEUE_URL=http://localstack.$(NAMESPACE_EVT).svc.cluster.local:4566/000000000000/db-migration-queue" \
			--env="OTEL_EXPORTER_OTLP_ENDPOINT=otel-collector.$(NAMESPACE_OBS).svc.cluster.local:4317" \
			--restart=Always 2>/dev/null || true

build-python:
	@echo ">>> Building Django migration service..."
	docker build -t $(DJANGO_IMAGE) python/django_migration/

load-python-image:
	kind load docker-image $(DJANGO_IMAGE) --name $(CLUSTER_NAME)

# =============================================================================
# テスト・動作確認
# =============================================================================

## API への各種テストリクエスト (一括実行)
test-request: test-recommend test-execute
	@echo "✅ All test requests sent."

## Feature Toggle: JP/TW/KR リージョンで推薦API をテスト
test-recommend:
	@echo "\n=== [JP Region] ==="
	@curl -s -H "X-Region: JP" http://localhost:8080/api/recommend | python3 -m json.tool || \
		kubectl -n $(NAMESPACE_APP) exec deploy/agent-backend -- \
			wget -qO- --header="X-Region: JP" http://localhost:8080/api/recommend
	@echo "\n=== [TW Region] ==="
	@curl -s -H "X-Region: TW" http://localhost:8080/api/recommend | python3 -m json.tool || true
	@echo "\n=== [KR Region] ==="
	@curl -s -H "X-Region: KR" http://localhost:8080/api/recommend | python3 -m json.tool || true

## Agent Sandbox: コード実行リクエストをテスト
test-execute:
	@echo "\n=== [Sandbox Execute] ==="
	curl -s -X POST http://localhost:8080/api/execute \
		-H "Content-Type: application/json" \
		-d '{"command": "echo hello from agent"}' | python3 -m json.tool || true

## SQSにマイグレーションイベントを送信 → KEDA ScaledJob 起動を確認
test-migration: send-migration-event
	@echo ">>> Waiting for KEDA to trigger migration job..."
	@sleep 20
	kubectl get jobs -n $(NAMESPACE_EVT) -l app=db-migrator
	kubectl logs -n $(NAMESPACE_EVT) -l app=db-migrator --tail=30 || true

## SQSにメッセージを直接送信
send-migration-event:
	@echo ">>> Sending migration event to SQS..."
	kubectl -n $(NAMESPACE_EVT) run sqs-sender --rm -it --restart=Never \
		--image=amazon/aws-cli:2.15.30 \
		--env="AWS_ACCESS_KEY_ID=test" \
		--env="AWS_SECRET_ACCESS_KEY=test" \
		--env="AWS_DEFAULT_REGION=ap-northeast-1" \
		-- aws --endpoint-url=http://localstack.$(NAMESPACE_EVT).svc.cluster.local:4566 \
		sqs send-message \
		--queue-url http://localstack.$(NAMESPACE_EVT).svc.cluster.local:4566/000000000000/db-migration-queue \
		--message-body '{"migration_id":"mig-$(shell date +%s)","db":"users-db","version":"20240101"}' \
		2>/dev/null || echo "Note: SQS send requires LocalStack to be running"

# =============================================================================
# ポートフォワード / ログ
# =============================================================================

## Go API をローカル 8080 に転送
port-forward-app:
	kubectl port-forward -n $(NAMESPACE_APP) svc/agent-backend 8080:80

## Jaeger UI を localhost:16686 に転送
port-forward-jaeger:
	@echo "Jaeger UI: http://localhost:16686"
	kubectl port-forward -n $(NAMESPACE_OBS) svc/jaeger-ui 16686:16686

## OTLP Collector を直接転送
port-forward-otel:
	kubectl port-forward -n $(NAMESPACE_OBS) svc/otel-collector 4317:4317

logs-backend:
	kubectl logs -n $(NAMESPACE_APP) -l app=agent-backend --follow --tail=50

logs-migration:
	kubectl logs -n $(NAMESPACE_EVT) -l app=db-migrator --tail=50

logs-otel:
	kubectl logs -n $(NAMESPACE_OBS) -l app=otel-collector --follow --tail=50

# =============================================================================
# ドキュメント
# =============================================================================

## ドキュメントをローカル HTTP サーバーで配信 (docs/index.html が README.md を動的描画)
docs-serve:
	@echo "📚 Docs: http://localhost:8888"
	@echo "   docs/index.html が README.md を fetch → marked.js + Mermaid.js で描画します"
	cd docs && python3 -m http.server 8888

# =============================================================================
# クリーンアップ
# =============================================================================

## Kind クラスタを削除
cluster-down:
	kind delete cluster --name $(CLUSTER_NAME)
	rm -f $(KIND_CONFIG)

clean: cluster-down
	docker rmi $(APP_IMAGE) $(DJANGO_IMAGE) 2>/dev/null || true

# =============================================================================
# ヘルプ
# =============================================================================
help:
	@echo "Usage: make [target]"
	@echo ""
	@echo "Cluster:"
	@echo "  cluster-up          Kind クラスタ作成 + Istio/KEDA インストール"
	@echo "  cluster-down        クラスタ削除"
	@echo ""
	@echo "Deploy:"
	@echo "  deploy              Go + Django をビルド → Kind にロード → K8s デプロイ"
	@echo "  deploy-python       Django のみデプロイ"
	@echo ""
	@echo "Test:"
	@echo "  test-request        推薦API + Sandbox を一括テスト"
	@echo "  test-recommend      Feature Toggle つき推薦API (JP/TW/KR)"
	@echo "  test-execute        Agent Sandbox 実行テスト"
	@echo "  test-migration      SQS→KEDA→Job の E2E テスト"
	@echo "  send-migration-event SQS にイベントを送信"
	@echo ""
	@echo "Debug:"
	@echo "  port-forward-app    Go API を localhost:8080 に転送"
	@echo "  port-forward-jaeger Jaeger UI を localhost:16686 に転送"
	@echo "  logs-backend        Go バックエンドのログをフォロー"
	@echo "  logs-migration      マイグレーション Job のログを表示"
	@echo ""
	@echo "Docs:"
	@echo "  docs-serve          ドキュメントを localhost:8888 で配信"
