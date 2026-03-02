# Nexora Super App (Flutter)

Interface universal responsiva com navegação principal:
- Pay
- Social
- Move
- Chat
- Menu

Regras aplicadas:
- Paleta obrigatória: `#050810`, `#00FF94`, `#00D4FF`.
- Botão `📄 Gerar Comprovante PDF` em telas financeiras.
- Botão de compra nativa nos vídeos do Social.
- Injeção de rastreio de pacote na tela do usuário.

## Execução local

```bash
flutter pub get
flutter run -d chrome --dart-define=NEXORA_API_BASE_URL=http://localhost
```

## Build

Use os scripts em `scripts/`:
- `build_android.sh`
- `build_web.sh`
- `build_windows.bat`
