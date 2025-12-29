# ============================================================================
# Trace Testing Script for MPiper
# Tests OpenTelemetry tracing integration with Tempo and Grafana
# ============================================================================

Write-Host "`n=== MPiper Trace Verification Script ===" -ForegroundColor Cyan
Write-Host "This script will help you verify that traces are working correctly.`n" -ForegroundColor Gray

# Configuration
$API_BASE_URL = "http://localhost:5010"
$GRAFANA_URL = "http://localhost:3000"
$TEMPO_URL = "http://localhost:3200"
$OTEL_COLLECTOR_URL = "http://localhost:13133"

# ============================================================================
# Step 1: Check if all observability services are running
# ============================================================================
Write-Host "[1/6] Checking observability services..." -ForegroundColor Yellow

$services = @{
    "Grafana" = $GRAFANA_URL
    "Tempo" = "$TEMPO_URL/ready"
    "OTEL Collector" = $OTEL_COLLECTOR_URL
}

$allHealthy = $true
foreach ($service in $services.GetEnumerator()) {
    try {
        $response = Invoke-WebRequest -Uri $service.Value -Method Get -TimeoutSec 5 -UseBasicParsing
        if ($response.StatusCode -eq 200) {
            Write-Host "  ✓ $($service.Key) is running" -ForegroundColor Green
        } else {
            Write-Host "  ✗ $($service.Key) returned status: $($response.StatusCode)" -ForegroundColor Red
            $allHealthy = $false
        }
    } catch {
        Write-Host "  ✗ $($service.Key) is not accessible: $($_.Exception.Message)" -ForegroundColor Red
        $allHealthy = $false
    }
}

if (-not $allHealthy) {
    Write-Host "`n⚠️  Some services are not running. Start them with:" -ForegroundColor Yellow
    Write-Host "   docker-compose -f deploy/docker/docker-compose.observability.yml up -d`n" -ForegroundColor Gray
    exit 1
}

# ============================================================================
# Step 2: Generate test traffic to create traces
# ============================================================================
Write-Host "`n[2/6] Generating test traffic..." -ForegroundColor Yellow

$testEndpoints = @(
    "/healthz",
    "/api/v1/assets",
    "/api/v1/assets/123"
)

Write-Host "  Sending requests to generate traces..." -ForegroundColor Gray
foreach ($endpoint in $testEndpoints) {
    try {
        $url = "$API_BASE_URL$endpoint"
        Invoke-WebRequest -Uri $url -Method Get -TimeoutSec 5 -UseBasicParsing -ErrorAction SilentlyContinue | Out-Null
        Write-Host "  ✓ Sent request to $endpoint" -ForegroundColor Green
        Start-Sleep -Milliseconds 500
    } catch {
        Write-Host "  ⚠️  Request to $endpoint failed (this is OK for testing): $($_.Exception.Message)" -ForegroundColor Yellow
    }
}

Write-Host "  Waiting 5 seconds for traces to propagate..." -ForegroundColor Gray
Start-Sleep -Seconds 5

# ============================================================================
# Step 3: Query Tempo API for traces
# ============================================================================
Write-Host "`n[3/6] Querying Tempo for traces..." -ForegroundColor Yellow

try {
    # Search for traces in the last 5 minutes
    $searchUrl = "$TEMPO_URL/api/search?tags=service.name=mpiper-api&start=$((Get-Date).AddMinutes(-5).ToUniversalTime().ToString('o'))&end=$((Get-Date).ToUniversalTime().ToString('o'))"
    
    $traceSearch = Invoke-RestMethod -Uri $searchUrl -Method Get -TimeoutSec 10
    
    if ($traceSearch.traces -and $traceSearch.traces.Count -gt 0) {
        Write-Host "  ✓ Found $($traceSearch.traces.Count) trace(s) in Tempo" -ForegroundColor Green
        
        # Get the first trace ID for detailed inspection
        $firstTraceId = $traceSearch.traces[0].traceID
        Write-Host "  ℹ️  Sample Trace ID: $firstTraceId" -ForegroundColor Cyan
        
        # Fetch full trace details
        $traceUrl = "$TEMPO_URL/api/traces/$firstTraceId"
        $traceDetails = Invoke-RestMethod -Uri $traceUrl -Method Get -TimeoutSec 10
        
        if ($traceDetails.batches) {
            $spanCount = ($traceDetails.batches.scopeSpans.spans | Measure-Object).Count
            Write-Host "  ✓ Trace contains $spanCount span(s)" -ForegroundColor Green
        }
    } else {
        Write-Host "  ✗ No traces found in Tempo" -ForegroundColor Red
        Write-Host "    This might mean traces are not being exported correctly." -ForegroundColor Yellow
    }
} catch {
    Write-Host "  ✗ Failed to query Tempo: $($_.Exception.Message)" -ForegroundColor Red
}

# ============================================================================
# Step 4: Check OTEL Collector metrics
# ============================================================================
Write-Host "`n[4/6] Checking OTEL Collector metrics..." -ForegroundColor Yellow

try {
    $metricsUrl = "http://localhost:8888/metrics"
    $metrics = Invoke-WebRequest -Uri $metricsUrl -Method Get -TimeoutSec 5 -UseBasicParsing
    
    if ($metrics.Content -match 'otelcol_receiver_accepted_spans') {
        $receivedSpans = [regex]::Match($metrics.Content, 'otelcol_receiver_accepted_spans{[^}]*}\s+(\d+)').Groups[1].Value
        Write-Host "  ✓ OTEL Collector has received $receivedSpans span(s)" -ForegroundColor Green
    }
    
    if ($metrics.Content -match 'otelcol_exporter_sent_spans') {
        $sentSpans = [regex]::Match($metrics.Content, 'otelcol_exporter_sent_spans{[^}]*exporter="otlp/tempo"[^}]*}\s+(\d+)').Groups[1].Value
        Write-Host "  ✓ OTEL Collector has sent $sentSpans span(s) to Tempo" -ForegroundColor Green
    }
} catch {
    Write-Host "  ⚠️  Could not fetch OTEL Collector metrics: $($_.Exception.Message)" -ForegroundColor Yellow
}

# ============================================================================
# Step 5: Verify Grafana datasource configuration
# ============================================================================
Write-Host "`n[5/6] Verifying Grafana datasource..." -ForegroundColor Yellow

try {
    # Note: This requires Grafana auth. Using default admin:admin
    $base64Auth = [Convert]::ToBase64String([Text.Encoding]::ASCII.GetBytes("admin:admin"))
    $headers = @{
        Authorization = "Basic $base64Auth"
    }
    
    $datasources = Invoke-RestMethod -Uri "$GRAFANA_URL/api/datasources" -Method Get -Headers $headers -TimeoutSec 5
    
    $tempoDatasource = $datasources | Where-Object { $_.type -eq "tempo" }
    if ($tempoDatasource) {
        Write-Host "  ✓ Tempo datasource is configured in Grafana" -ForegroundColor Green
        
        # Test datasource connectivity
        $testUrl = "$GRAFANA_URL/api/datasources/uid/$($tempoDatasource.uid)/health"
        $healthCheck = Invoke-RestMethod -Uri $testUrl -Method Get -Headers $headers -TimeoutSec 5
        
        if ($healthCheck.status -eq "OK") {
            Write-Host "  ✓ Tempo datasource is healthy" -ForegroundColor Green
        } else {
            Write-Host "  ⚠️  Tempo datasource health check returned: $($healthCheck.status)" -ForegroundColor Yellow
        }
    } else {
        Write-Host "  ✗ Tempo datasource not found in Grafana" -ForegroundColor Red
    }
} catch {
    Write-Host "  ⚠️  Could not verify Grafana datasource: $($_.Exception.Message)" -ForegroundColor Yellow
    Write-Host "     Default credentials (admin:admin) might not work." -ForegroundColor Gray
}

# ============================================================================
# Step 6: Provide next steps
# ============================================================================
Write-Host "`n[6/6] Next Steps" -ForegroundColor Yellow
Write-Host @"
  
To view traces in Grafana:
  
  1. Open Grafana: $GRAFANA_URL
  2. Navigate to: Explore → Select 'Tempo' datasource
  3. Use TraceQL query:
     {service.name="mpiper-api"}
  
  4. Or search by:
     - Service Name: mpiper-api
     - Time range: Last 15 minutes
  
  5. Click on any trace to see the full flame graph

Useful URLs:
  • Grafana Explore:     $GRAFANA_URL/explore
  • Tempo UI:            $TEMPO_URL
  • OTEL Collector:      http://localhost:8888/metrics
  • OTEL zPages:         http://localhost:55679/debug/tracez

"@ -ForegroundColor Gray

Write-Host "=== Verification Complete ===`n" -ForegroundColor Cyan
