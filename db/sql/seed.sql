BEGIN;

CREATE TABLE IF NOT EXISTS wallets (
  user_id TEXT PRIMARY KEY,
  brl_cents BIGINT NOT NULL DEFAULT 0,
  nex_units BIGINT NOT NULL DEFAULT 0,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS wallet_transactions (
  id BIGSERIAL PRIMARY KEY,
  tx_type TEXT NOT NULL,
  from_user_id TEXT,
  to_user_id TEXT,
  pix_key TEXT,
  currency TEXT NOT NULL,
  amount_raw BIGINT NOT NULL CHECK (amount_raw > 0),
  status TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS stock_imported_products (
  product_id TEXT PRIMARY KEY,
  source TEXT NOT NULL,
  external_id TEXT NOT NULL,
  category TEXT NOT NULL,
  title TEXT NOT NULL,
  currency TEXT NOT NULL,
  cost_cents BIGINT NOT NULL,
  freight_cents BIGINT NOT NULL,
  margin_cents BIGINT NOT NULL,
  final_price_cents BIGINT NOT NULL,
  product_url TEXT NOT NULL DEFAULT '',
  image_url TEXT NOT NULL DEFAULT '',
  imported_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  raw_json JSONB NOT NULL DEFAULT '{}'::jsonb
);

CREATE UNIQUE INDEX IF NOT EXISTS ux_stock_source_external_category
  ON stock_imported_products(source, external_id, category);

CREATE TABLE IF NOT EXISTS social_seed_videos (
  video_id TEXT PRIMARY KEY,
  creator_id TEXT NOT NULL,
  title TEXT NOT NULL,
  description TEXT NOT NULL,
  category TEXT NOT NULL,
  tags TEXT[] NOT NULL DEFAULT ARRAY[]::TEXT[],
  linked_product_id TEXT NOT NULL,
  duration_seconds INT NOT NULL,
  published_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

INSERT INTO wallets (user_id, brl_cents, nex_units, updated_at)
VALUES ('anderson', 10000000, 500000000, NOW())
ON CONFLICT (user_id)
DO UPDATE SET
  brl_cents = EXCLUDED.brl_cents,
  nex_units = EXCLUDED.nex_units,
  updated_at = NOW();

INSERT INTO wallet_transactions (tx_type, from_user_id, to_user_id, currency, amount_raw, status)
SELECT 'seed_credit', 'seed-treasury', 'anderson', 'BRL', 10000000, 'completed'
WHERE NOT EXISTS (
  SELECT 1
  FROM wallet_transactions
  WHERE tx_type = 'seed_credit'
    AND from_user_id = 'seed-treasury'
    AND to_user_id = 'anderson'
    AND currency = 'BRL'
    AND amount_raw = 10000000
    AND status = 'completed'
);

INSERT INTO stock_imported_products (
  product_id,
  source,
  external_id,
  category,
  title,
  currency,
  cost_cents,
  freight_cents,
  margin_cents,
  final_price_cents,
  product_url,
  image_url,
  imported_at,
  raw_json
)
VALUES
  ('prd-amazon-fitband-001-fitness', 'amazon', 'fitband-001', 'fitness', 'Smart Fit Band Pro', 'BRL', 18000, 2200, 10100, 30300, 'https://amazon.example/products/fitband-001', 'https://images.example/amazon-fitband.jpg', NOW(), '{"source":"amazon","seed":true}'::jsonb),
  ('prd-amazon-yogamat-002-fitness', 'amazon', 'yogamat-002', 'fitness', 'Yoga Mat Premium', 'BRL', 9000, 1600, 5300, 15900, 'https://amazon.example/products/yogamat-002', 'https://images.example/amazon-yogamat.jpg', NOW(), '{"source":"amazon","seed":true}'::jsonb),

  ('prd-alibaba-ringlight-011-home', 'alibaba', 'ringlight-011', 'home', 'Ring Light Studio 16', 'BRL', 14000, 2900, 8450, 25350, 'https://alibaba.example/products/ringlight-011', 'https://images.example/alibaba-ringlight.jpg', NOW(), '{"source":"alibaba","seed":true}'::jsonb),
  ('prd-alibaba-miniblender-012-home', 'alibaba', 'miniblender-012', 'home', 'Mini Blender USB', 'BRL', 11000, 2500, 6750, 20250, 'https://alibaba.example/products/miniblender-012', 'https://images.example/alibaba-blender.jpg', NOW(), '{"source":"alibaba","seed":true}'::jsonb),

  ('prd-cj-neckmassager-021-wellness', 'cj-dropshipping', 'neckmassager-021', 'wellness', 'Neck Massager Deep Pulse', 'BRL', 20000, 3400, 11700, 35100, 'https://cj.example/products/neckmassager-021', 'https://images.example/cj-neckmassager.jpg', NOW(), '{"source":"cj-dropshipping","seed":true}'::jsonb),
  ('prd-cj-posturecorrector-022-wellness', 'cj-dropshipping', 'posturecorrector-022', 'wellness', 'Posture Corrector Smart', 'BRL', 13000, 3000, 8000, 24000, 'https://cj.example/products/posturecorrector-022', 'https://images.example/cj-posture.jpg', NOW(), '{"source":"cj-dropshipping","seed":true}'::jsonb),

  ('prd-aliexpress-powerbank-031-electronics', 'aliexpress', 'powerbank-031', 'electronics', 'Power Bank 20000mAh', 'BRL', 15000, 2600, 8800, 26400, 'https://aliexpress.example/products/powerbank-031', 'https://images.example/ali-powerbank.jpg', NOW(), '{"source":"aliexpress","seed":true}'::jsonb),
  ('prd-aliexpress-mic-032-electronics', 'aliexpress', 'mic-032', 'electronics', 'Microfone Lapela Pro', 'BRL', 8000, 2200, 5100, 15300, 'https://aliexpress.example/products/mic-032', 'https://images.example/ali-mic.jpg', NOW(), '{"source":"aliexpress","seed":true}'::jsonb),

  ('prd-mercadolivre-headset-041-gaming', 'mercadolivre', 'headset-041', 'gaming', 'Headset Gamer RGB', 'BRL', 17000, 1900, 9450, 28350, 'https://mercadolivre.example/products/headset-041', 'https://images.example/ml-headset.jpg', NOW(), '{"source":"mercadolivre","seed":true}'::jsonb),
  ('prd-mercadolivre-keyboard-042-gaming', 'mercadolivre', 'keyboard-042', 'gaming', 'Teclado Mecânico ABNT2', 'BRL', 21000, 2100, 11550, 34650, 'https://mercadolivre.example/products/keyboard-042', 'https://images.example/ml-keyboard.jpg', NOW(), '{"source":"mercadolivre","seed":true}'::jsonb),

  ('prd-shopee-tripod-051-creator', 'shopee', 'tripod-051', 'creator', 'Tripé Flex 360', 'BRL', 7000, 1700, 4350, 13050, 'https://shopee.example/products/tripod-051', 'https://images.example/shopee-tripod.jpg', NOW(), '{"source":"shopee","seed":true}'::jsonb),
  ('prd-shopee-ledpanel-052-creator', 'shopee', 'ledpanel-052', 'creator', 'Painel LED Portátil', 'BRL', 12500, 1800, 7150, 21450, 'https://shopee.example/products/ledpanel-052', 'https://images.example/shopee-ledpanel.jpg', NOW(), '{"source":"shopee","seed":true}'::jsonb)
ON CONFLICT (product_id) DO UPDATE SET
  title = EXCLUDED.title,
  cost_cents = EXCLUDED.cost_cents,
  freight_cents = EXCLUDED.freight_cents,
  margin_cents = EXCLUDED.margin_cents,
  final_price_cents = EXCLUDED.final_price_cents,
  product_url = EXCLUDED.product_url,
  image_url = EXCLUDED.image_url,
  imported_at = NOW(),
  raw_json = EXCLUDED.raw_json;

INSERT INTO social_seed_videos (
  video_id,
  creator_id,
  title,
  description,
  category,
  tags,
  linked_product_id,
  duration_seconds,
  published_at
)
VALUES
  ('social-seed-001', 'creator-anderson', 'Treino 12min em Casa', 'Treino rápido com foco em resistência e cardio.', 'fitness', ARRAY['fitness', 'home', 'shorts'], 'prd-amazon-fitband-001-fitness', 21, NOW()),
  ('social-seed-002', 'creator-maya', 'Setup de Luz para Reels', 'Como montar iluminação de baixo custo para vídeos.', 'creator', ARRAY['creator', 'studio', 'reels'], 'prd-alibaba-ringlight-011-home', 24, NOW()),
  ('social-seed-003', 'creator-juliano', 'Anti-stress no Fim de Semana', 'Rotina curta de recuperação e mobilidade.', 'wellness', ARRAY['wellness', 'recovery', 'weekend'], 'prd-cj-neckmassager-021-wellness', 18, NOW()),
  ('social-seed-004', 'creator-luiza', 'Kit Mobile para Gravação', 'Itens essenciais para gravar com qualidade no celular.', 'electronics', ARRAY['creator', 'electronics', 'mobile'], 'prd-aliexpress-mic-032-electronics', 20, NOW()),
  ('social-seed-005', 'creator-arthur', 'Battle Setup Gamer', 'Upgrade de periféricos com melhor custo-benefício.', 'gaming', ARRAY['gaming', 'gear', 'setup'], 'prd-mercadolivre-keyboard-042-gaming', 26, NOW()),
  ('social-seed-006', 'creator-bea', 'Iluminação Portátil para Vlog', 'Painel LED compacto para gravações externas.', 'creator', ARRAY['vlog', 'creator', 'outdoor'], 'prd-shopee-ledpanel-052-creator', 22, NOW())
ON CONFLICT (video_id) DO UPDATE SET
  title = EXCLUDED.title,
  description = EXCLUDED.description,
  category = EXCLUDED.category,
  tags = EXCLUDED.tags,
  linked_product_id = EXCLUDED.linked_product_id,
  duration_seconds = EXCLUDED.duration_seconds,
  published_at = NOW();

COMMIT;
