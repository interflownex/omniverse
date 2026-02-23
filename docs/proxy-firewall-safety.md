# Proxy and Firewall Safety

NEXORA local profile does **not** apply global OS proxy configuration and does **not** create persistent firewall rules.

Validation:

```powershell
.\scripts\validate-host-internet.ps1
```

Use the command before and after remote publication scripts to confirm host internet remains unaffected.
