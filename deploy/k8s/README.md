# MPiper Kubernetes Deployment Guide

This directory contains Kubernetes manifests for deploying MPiper in a production environment.

## Prerequisites

- Kubernetes cluster (v1.24+)
- `kubectl` configured to access your cluster
- Container images built and pushed to a registry
- GCS service account credentials JSON file

## Quick Start

### 1. Create the Namespace

```bash
kubectl apply -f namespace.yaml
```

### 2. Create Secrets

**Database and Redis secrets:**
```bash
# Edit secrets.yaml with your actual credentials before applying
kubectl apply -f secrets.yaml
```

**GCS Credentials:**
```bash
kubectl create secret generic gcs-credentials \
  --from-file=key.json=./path/to/your/service-account.json \
  -n mpiper
```

### 3. Update ConfigMap

Edit `configmap.yaml` with your specific configuration (bucket names, endpoints, etc.), then apply:

```bash
kubectl apply -f configmap.yaml
```

### 4. Create Persistent Volume Claims

```bash
kubectl apply -f pvc.yaml
```

### 5. Deploy Infrastructure (PostgreSQL & Redis)

```bash
kubectl apply -f postgres.yaml
kubectl apply -f redis.yaml
```

Wait for the pods to be ready:
```bash
kubectl wait --for=condition=ready pod -l app=postgres -n mpiper --timeout=300s
kubectl wait --for=condition=ready pod -l app=redis -n mpiper --timeout=300s
```

### 6. Run Database Migrations

```bash
kubectl apply -f migration-job.yaml
```

Check migration status:
```bash
kubectl logs -f job/mpiper-db-migration -n mpiper
```

### 7. Deploy Application Services

```bash
kubectl apply -f rbac.yaml
kubectl apply -f api-deployment.yaml
kubectl apply -f worker-deployment.yaml
```

### 8. Configure Ingress (Optional)

Update `ingress.yaml` with your domain and apply:

```bash
kubectl apply -f ingress.yaml
```

## Verify Deployment

```bash
# Check all pods
kubectl get pods -n mpiper

# Check services
kubectl get svc -n mpiper

# Check deployments
kubectl get deployments -n mpiper

# Check horizontal pod autoscalers
kubectl get hpa -n mpiper
```

## Access the API

**Via LoadBalancer:**
```bash
kubectl get svc mpiper-api-service -n mpiper
# Use the EXTERNAL-IP shown
```

**Via Port Forward (for testing):**
```bash
kubectl port-forward svc/mpiper-api-service 8080:80 -n mpiper
# Access at http://localhost:8080
```

## Scaling

### Manual Scaling

```bash
# Scale API
kubectl scale deployment mpiper-api --replicas=5 -n mpiper

# Scale Worker
kubectl scale deployment mpiper-worker --replicas=5 -n mpiper
```

### Auto-scaling

HPA is configured by default:
- **API**: 3-10 replicas based on CPU (70%) and Memory (80%)
- **Worker**: 2-10 replicas based on CPU (75%) and Memory (85%)

## Monitoring

```bash
# View API logs
kubectl logs -f deployment/mpiper-api -n mpiper

# View Worker logs
kubectl logs -f deployment/mpiper-worker -n mpiper

# View specific pod logs
kubectl logs -f <pod-name> -n mpiper
```

## Updating

### Update API

```bash
# Build and push new image
docker build -t your-registry/mpiper-api:v1.1.0 -f mpiper.dockerfile .
docker push your-registry/mpiper-api:v1.1.0

# Update deployment
kubectl set image deployment/mpiper-api mpiper-api=your-registry/mpiper-api:v1.1.0 -n mpiper

# Watch rollout
kubectl rollout status deployment/mpiper-api -n mpiper
```

### Update Worker

```bash
# Build and push new image
docker build -t your-registry/mpiper-worker:v1.1.0 -f deploy/docker/worker.dockerfile .
docker push your-registry/mpiper-worker:v1.1.0

# Update deployment
kubectl set image deployment/mpiper-worker mpiper-worker=your-registry/mpiper-worker:v1.1.0 -n mpiper

# Watch rollout
kubectl rollout status deployment/mpiper-worker -n mpiper
```

## Rollback

```bash
# Rollback API
kubectl rollout undo deployment/mpiper-api -n mpiper

# Rollback Worker
kubectl rollout undo deployment/mpiper-worker -n mpiper
```

## Cleanup

```bash
# Delete all resources
kubectl delete namespace mpiper

# Or delete individual components
kubectl delete -f api-deployment.yaml
kubectl delete -f worker-deployment.yaml
kubectl delete -f postgres.yaml
kubectl delete -f redis.yaml
```

## Configuration Files

- `namespace.yaml` - Creates the mpiper namespace
- `configmap.yaml` - Environment configuration
- `secrets.yaml` - Sensitive credentials (template)
- `pvc.yaml` - Persistent volume claims for PostgreSQL and Redis
- `postgres.yaml` - PostgreSQL database deployment
- `redis.yaml` - Redis cache deployment
- `api-deployment.yaml` - Go API server deployment with HPA
- `worker-deployment.yaml` - Python worker deployment with HPA
- `rbac.yaml` - Service accounts and RBAC policies
- `ingress.yaml` - Ingress configuration for external access
- `migration-job.yaml` - Database migration job

## Resource Limits

### API Server (per pod)
- Requests: 100m CPU, 128Mi Memory
- Limits: 1000m CPU, 512Mi Memory

### Worker (per pod)
- Requests: 250m CPU, 256Mi Memory
- Limits: 2000m CPU, 2Gi Memory

### PostgreSQL
- Requests: 250m CPU, 256Mi Memory
- Limits: 1000m CPU, 1Gi Memory
- Storage: 20Gi

### Redis
- Requests: 100m CPU, 128Mi Memory
- Limits: 500m CPU, 512Mi Memory
- Storage: 5Gi

## Troubleshooting

### Pods not starting

```bash
kubectl describe pod <pod-name> -n mpiper
kubectl logs <pod-name> -n mpiper
```

### Database connection issues

```bash
# Check if postgres is ready
kubectl get pods -l app=postgres -n mpiper

# Test connection
kubectl exec -it deployment/mpiper-api -n mpiper -- sh
# (if your image has shell access)
```

### Worker not processing jobs

```bash
# Check worker logs
kubectl logs -f deployment/mpiper-worker -n mpiper

# Check Redis connectivity
kubectl exec -it deployment/redis -n mpiper -- redis-cli ping
```

### Checking secrets

```bash
kubectl get secrets -n mpiper
kubectl describe secret mpiper-secrets -n mpiper
```

## Production Recommendations

1. **Use a managed database** (Cloud SQL, RDS) instead of PostgreSQL pod
2. **Use managed Redis** (ElastiCache, Memorystore) for better reliability
3. **Enable SSL/TLS** for all connections
4. **Set up monitoring** with Prometheus/Grafana
5. **Configure log aggregation** (ELK, Loki, Cloud Logging)
6. **Implement backup strategies** for persistent data
7. **Use network policies** for pod-to-pod communication
8. **Set resource quotas** at namespace level
9. **Configure pod disruption budgets** for high availability
10. **Use external secrets** management (Vault, Cloud Secret Manager)

## Security Notes

- Change all default passwords in `secrets.yaml`
- Restrict network access using NetworkPolicies
- Use RBAC to limit pod permissions
- Regularly update container images
- Scan images for vulnerabilities
- Use private container registry
- Enable pod security policies/standards

