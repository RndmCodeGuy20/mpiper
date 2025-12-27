# Test script to verify OpenTelemetry metrics are working

Write-Host "Testing OpenTelemetry Metrics Setup" -ForegroundColor Cyan
Write-Host "====================================" -ForegroundColor Cyan
Write-Host ""

# Function to test endpoint
function Test-Endpoint {
    param (
        [string]$Url,
        [string]$Description
    )
    
    Write-Host "Testing: $Description" -ForegroundColor Yellow
    Write-Host "URL: $Url" -ForegroundColor Gray
    
    try {
        $response = Invoke-WebRequest -Uri $Url -UseBasicParsing -TimeoutSec 5 -ErrorAction Stop
        Write-Host "✓ Status: $($response.StatusCode)" -ForegroundColor Green
        return $true
    }
    catch {
        Write-Host "✗ Failed: $($_.Exception.Message)" -ForegroundColor Red
        return $false
    }
    Write-Host ""
}

# Wait for services to be ready
Write-Host "Waiting for services to start..." -ForegroundColor Yellow
Start-Sleep -Seconds 5

# Test API endpoints to generate metrics
Write-Host "`n1. Generating test requests to API..." -ForegroundColor Cyan
Write-Host "--------------------------------------" -ForegroundColor Gray

$apiBaseUrl = "http://localhost:8080"

Test-Endpoint "$apiBaseUrl/" "Root endpoint"
Test-Endpoint "$apiBaseUrl/api/v1" "API v1 endpoint"
Test-Endpoint "$apiBaseUrl/api/v1/status" "Status endpoint"

# Generate multiple requests to see metric changes
Write-Host "`n2. Generating additional traffic..." -ForegroundColor Cyan
Write-Host "------------------------------------" -ForegroundColor Gray

for ($i = 1; $i -le 5; $i++) {
    Write-Host "Request $i of 5..." -ForegroundColor Gray
    Invoke-WebRequest -Uri "$apiBaseUrl/api/v1/status" -UseBasicParsing -TimeoutSec 5 -ErrorAction SilentlyContinue | Out-Null
    Start-Sleep -Milliseconds 500
}

Write-Host "✓ Traffic generation complete" -ForegroundColor Green

# Check OTel Collector
Write-Host "`n3. Checking OpenTelemetry Collector..." -ForegroundColor Cyan
Write-Host "----------------------------------------" -ForegroundColor Gray

Test-Endpoint "http://localhost:13133" "OTel Collector Health"
Test-Endpoint "http://localhost:9464/metrics" "OTel Collector Prometheus Exporter"

# Check Prometheus
Write-Host "`n4. Checking Prometheus..." -ForegroundColor Cyan
Write-Host "--------------------------" -ForegroundColor Gray

Test-Endpoint "http://localhost:9090/-/healthy" "Prometheus Health"
Test-Endpoint "http://localhost:9090/api/v1/targets" "Prometheus Targets"

# Query for our specific metric
Write-Host "`n5. Querying for HTTP request metrics..." -ForegroundColor Cyan
Write-Host "-----------------------------------------" -ForegroundColor Gray

$metricsToCheck = @(
    "mpiper_http_server_request_duration",
    "mpiper_http_server_request_count"
)

foreach ($metric in $metricsToCheck) {
    Write-Host "`nChecking metric: $metric" -ForegroundColor Yellow
    
    $query = [System.Web.HttpUtility]::UrlEncode($metric)
    $url = "http://localhost:9090/api/v1/query?query=$query"
    
    try {
        $response = Invoke-RestMethod -Uri $url -Method Get -TimeoutSec 5
        
        if ($response.status -eq "success" -and $response.data.result.Count -gt 0) {
            Write-Host "✓ Metric found in Prometheus!" -ForegroundColor Green
            Write-Host "  Result count: $($response.data.result.Count)" -ForegroundColor Gray
            
            # Show first few results
            $response.data.result | Select-Object -First 3 | ForEach-Object {
                $labels = ($_.metric.PSObject.Properties | Where-Object { $_.Name -ne "__name__" } | ForEach-Object { "$($_.Name)=$($_.Value)" }) -join ", "
                $value = $_.value[1]
                Write-Host "  - {$labels} = $value" -ForegroundColor Gray
            }
        }
        else {
            Write-Host "✗ Metric not found or no data yet" -ForegroundColor Red
            Write-Host "  This might be normal if the collector hasn't scraped yet" -ForegroundColor Yellow
            Write-Host "  Wait 15-30 seconds and try querying Prometheus directly" -ForegroundColor Yellow
        }
    }
    catch {
        Write-Host "✗ Query failed: $($_.Exception.Message)" -ForegroundColor Red
    }
}

# Summary
Write-Host "`n======================================" -ForegroundColor Cyan
Write-Host "Testing Complete!" -ForegroundColor Cyan
Write-Host "======================================" -ForegroundColor Cyan
Write-Host ""
Write-Host "Next Steps:" -ForegroundColor Yellow
Write-Host "1. Open Prometheus UI: http://localhost:9090" -ForegroundColor White
Write-Host "2. In the query box, try these queries:" -ForegroundColor White
Write-Host "   - mpiper_http_server_request_duration_sum" -ForegroundColor Gray
Write-Host "   - mpiper_http_server_request_count_total" -ForegroundColor Gray
Write-Host "   - rate(mpiper_http_server_request_duration_sum[5m])" -ForegroundColor Gray
Write-Host "3. Check OTel Collector metrics: http://localhost:9464/metrics" -ForegroundColor White
Write-Host "4. View Grafana dashboards: http://localhost:3000" -ForegroundColor White
Write-Host ""
