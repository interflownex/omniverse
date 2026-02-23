# Security Baseline

- Zero-trust namespace policy by default (`default-deny`).
- Explicit ingress allow-list for `nexora-core` on TCP 8080.
- mTLS should be enabled via service mesh in global clusters.
- Secret rotation target: every 90 days.
- Edge WAF + rate limiting required for public exposure.
