# Global Scalability Blueprint

## Regions
- us-east (primary)
- eu-west (secondary active)
- sa-east (latency + DR)

## Strategy
- Active-active reads
- Active-primary writes by tenant routing policy
- Regional failover target: RTO <= 15 minutes, RPO <= 5 minutes

## Capacity Targets
- 10,000 req/s global
- 1,000,000 tenants
- 50,000 concurrent sessions per region
- p95 read <= 250ms, write <= 450ms

## Autoscaling rules
- Scale out when CPU > 60% for 5 minutes
- Scale out when latency p95 exceeds target for 3 windows
- Conservative scale in with 10 minute cooldown
