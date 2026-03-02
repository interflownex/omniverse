const express = require('express');
const cors = require('cors');
const fs = require('fs');
const path = require('path');
const { v4: uuidv4 } = require('uuid');
const { S3Client, PutObjectCommand } = require('@aws-sdk/client-s3');
const { getSignedUrl } = require('@aws-sdk/s3-request-presigner');

const app = express();
app.use(cors());
app.use(express.json({ limit: '2mb' }));

const PORT = Number(process.env.PORT || '8085');
const MINIO_ENDPOINT = process.env.MINIO_ENDPOINT || 'http://minio:9000';
const MINIO_REGION = process.env.MINIO_REGION || 'us-east-1';
const MINIO_ACCESS_KEY = process.env.MINIO_ACCESS_KEY || 'nexora';
const MINIO_SECRET_KEY = process.env.MINIO_SECRET_KEY || 'nexora123';
const MINIO_BUCKET = process.env.MINIO_BUCKET || 'nexora-videos';
const MINIO_PUBLIC_BASE_URL = process.env.MINIO_PUBLIC_BASE_URL || MINIO_ENDPOINT;
const SOCIAL_API_BASE = process.env.SOCIAL_API_BASE || 'http://nexora-social:8084';
const SOCIAL_INGEST_TOKEN = process.env.SOCIAL_INGEST_TOKEN || 'social-ingest-token';
const MAX_UPLOAD_MB = Number(process.env.MAX_UPLOAD_MB || '250');
const MEDIA_DATA_FILE = process.env.MEDIA_DATA_FILE || path.join(process.cwd(), 'data', 'media-store.json');

const s3 = new S3Client({
  region: MINIO_REGION,
  endpoint: MINIO_ENDPOINT,
  forcePathStyle: true,
  credentials: {
    accessKeyId: MINIO_ACCESS_KEY,
    secretAccessKey: MINIO_SECRET_KEY
  }
});

app.use('/panel', express.static(path.join(__dirname, 'public')));

function ensureStoreFile() {
  const dir = path.dirname(MEDIA_DATA_FILE);
  fs.mkdirSync(dir, { recursive: true });
  if (!fs.existsSync(MEDIA_DATA_FILE)) {
    fs.writeFileSync(
      MEDIA_DATA_FILE,
      JSON.stringify({ videos: [], creators: {}, monetization: {} }, null, 2),
      'utf8'
    );
  }
}

function loadStore() {
  ensureStoreFile();
  const raw = fs.readFileSync(MEDIA_DATA_FILE, 'utf8');
  const parsed = JSON.parse(raw || '{}');
  return {
    videos: Array.isArray(parsed.videos) ? parsed.videos : [],
    creators: parsed.creators && typeof parsed.creators === 'object' ? parsed.creators : {},
    monetization: parsed.monetization && typeof parsed.monetization === 'object' ? parsed.monetization : {}
  };
}

function saveStore(store) {
  ensureStoreFile();
  const tmpPath = `${MEDIA_DATA_FILE}.tmp`;
  fs.writeFileSync(tmpPath, JSON.stringify(store, null, 2), 'utf8');
  fs.renameSync(tmpPath, MEDIA_DATA_FILE);
}

function normalizeAudience(input) {
  const v = String(input || '').trim().toLowerCase();
  if (v === 'personal' || v === 'professional') return v;
  return 'all';
}

function toNumber(value, fallback = 0) {
  const n = Number(value);
  return Number.isFinite(n) ? n : fallback;
}

function parseLimit(raw, fallback = 20, max = 100) {
  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return fallback;
  return Math.min(Math.max(Math.floor(n), 1), max);
}

function encodeCursor(record) {
  const payload = {
    published_at: record.published_at,
    id: record.id
  };
  return Buffer.from(JSON.stringify(payload), 'utf8').toString('base64url');
}

function decodeCursor(raw) {
  if (!raw) return null;
  try {
    const decoded = Buffer.from(String(raw), 'base64url').toString('utf8');
    const parsed = JSON.parse(decoded);
    if (!parsed.id || !parsed.published_at) return null;
    return parsed;
  } catch {
    return null;
  }
}

function sortVideosDesc(videos) {
  videos.sort((a, b) => {
    const at = new Date(a.published_at).getTime();
    const bt = new Date(b.published_at).getTime();
    if (at === bt) {
      return String(b.id).localeCompare(String(a.id));
    }
    return bt - at;
  });
}

function filterAfterCursor(videos, cursor) {
  if (!cursor) return videos;
  const cursorTime = new Date(cursor.published_at).getTime();
  const cursorId = String(cursor.id);
  return videos.filter((v) => {
    const t = new Date(v.published_at).getTime();
    if (t < cursorTime) return true;
    if (t === cursorTime && String(v.id) < cursorId) return true;
    return false;
  });
}

async function pushToSocial(video) {
  if (typeof fetch !== 'function') {
    return { ok: false, error: 'node runtime does not expose fetch' };
  }

  const payload = {
    id: video.id,
    creator_id: video.creator_id,
    title: video.title,
    description: video.description,
    object_key: video.object_key,
    duration_seconds: video.duration_seconds,
    audience: video.audience,
    tags: Array.isArray(video.tags) ? video.tags : [],
    published_at: video.published_at,
    views: video.views || 0,
    likes: video.likes || 0,
    monetization: video.monetization || {
      enabled: false,
      model: 'off',
      cpm_usd: 0,
      rev_share_pct: 0
    }
  };

  try {
    const resp = await fetch(`${SOCIAL_API_BASE}/v1/videos`, {
      method: 'POST',
      headers: {
        'content-type': 'application/json',
        'x-ingest-token': SOCIAL_INGEST_TOKEN
      },
      body: JSON.stringify(payload)
    });

    const body = await resp.text();
    if (!resp.ok) {
      return { ok: false, error: `social_ingest_failed status=${resp.status} body=${body}` };
    }
    return { ok: true };
  } catch (err) {
    return { ok: false, error: `social_ingest_unreachable error=${err.message}` };
  }
}

app.get('/healthz', (_req, res) => {
  res.json({
    status: 'ok',
    service: 'nexora-media',
    minio_bucket: MINIO_BUCKET,
    social_api_base: SOCIAL_API_BASE
  });
});

app.get('/', (_req, res) => {
  res.redirect('/panel/');
});

app.get('/v1/panel', (_req, res) => {
  res.sendFile(path.join(__dirname, 'public', 'index.html'));
});

app.post('/v1/videos/upload-url', async (req, res) => {
  const creatorId = String(req.body.creator_id || '').trim();
  const title = String(req.body.title || '').trim();
  const fileName = String(req.body.file_name || '').trim();
  const fileSizeBytes = Number(req.body.file_size_bytes || 0);
  const uploadMime = String(req.body.content_type || 'video/mp4').trim();

  if (!creatorId || !title || !fileName) {
    return res.status(400).json({ error: 'creator_id, title and file_name are required' });
  }
  if (!fileName.toLowerCase().endsWith('.mp4')) {
    return res.status(400).json({ error: 'only mp4 files are accepted' });
  }
  if (!Number.isFinite(fileSizeBytes) || fileSizeBytes <= 0) {
    return res.status(400).json({ error: 'file_size_bytes must be greater than zero' });
  }
  if (fileSizeBytes > MAX_UPLOAD_MB * 1024 * 1024) {
    return res.status(400).json({ error: `file_size_bytes exceeds MAX_UPLOAD_MB (${MAX_UPLOAD_MB}MB)` });
  }

  const videoId = String(req.body.video_id || uuidv4());
  const datePath = new Date().toISOString().slice(0, 10).replace(/-/g, '/');
  const objectKey = String(req.body.object_key || `${creatorId}/${datePath}/${videoId}.mp4`).replace(/^\/+/, '');

  const command = new PutObjectCommand({
    Bucket: MINIO_BUCKET,
    Key: objectKey,
    ContentType: uploadMime,
    Metadata: {
      creator_id: creatorId,
      video_id: videoId
    }
  });

  try {
    const uploadUrl = await getSignedUrl(s3, command, { expiresIn: 900 });
    return res.status(201).json({
      video_id: videoId,
      object_key: objectKey,
      upload_url: uploadUrl,
      method: 'PUT',
      required_headers: {
        'content-type': uploadMime
      },
      expires_in_seconds: 900
    });
  } catch (err) {
    return res.status(500).json({ error: `failed to create upload url: ${err.message}` });
  }
});

app.post('/v1/videos/register', async (req, res) => {
  const creatorId = String(req.body.creator_id || '').trim();
  const title = String(req.body.title || '').trim();
  const description = String(req.body.description || '').trim();
  const objectKey = String(req.body.object_key || '').trim().replace(/^\/+/, '');
  const durationSeconds = Math.floor(toNumber(req.body.duration_seconds, 0));
  const audience = normalizeAudience(req.body.audience);
  const tags = Array.isArray(req.body.tags) ? req.body.tags.map((t) => String(t).trim().toLowerCase()).filter(Boolean) : [];

  if (!creatorId || !title || !objectKey) {
    return res.status(400).json({ error: 'creator_id, title and object_key are required' });
  }
  if (!durationSeconds || durationSeconds <= 0) {
    return res.status(400).json({ error: 'duration_seconds must be greater than zero' });
  }

  const videoId = String(req.body.video_id || uuidv4());
  const nowISO = new Date().toISOString();

  const monetization = {
    enabled: Boolean(req.body.monetization?.enabled ?? true),
    model: String(req.body.monetization?.model || 'ad_cpm').trim() || 'ad_cpm',
    cpm_usd: Math.max(0, toNumber(req.body.monetization?.cpm_usd, 1.25)),
    rev_share_pct: Math.min(100, Math.max(0, toNumber(req.body.monetization?.rev_share_pct, 62.5)))
  };
  if (!monetization.enabled) {
    monetization.model = 'off';
    monetization.cpm_usd = 0;
    monetization.rev_share_pct = 0;
  }

  const store = loadStore();
  if (store.videos.some((v) => v.id === videoId)) {
    return res.status(409).json({ error: 'video_id already exists' });
  }

  const video = {
    id: videoId,
    creator_id: creatorId,
    title,
    description,
    object_key: objectKey,
    video_url: `${MINIO_PUBLIC_BASE_URL.replace(/\/$/, '')}/${MINIO_BUCKET}/${objectKey}`,
    duration_seconds: durationSeconds,
    audience,
    tags,
    status: 'ready',
    created_at: nowISO,
    published_at: nowISO,
    views: 0,
    likes: 0,
    monetization
  };

  store.videos.push(video);
  store.creators[creatorId] = store.creators[creatorId] || {
    creator_id: creatorId,
    created_at: nowISO
  };
  store.monetization[videoId] = monetization;
  sortVideosDesc(store.videos);
  saveStore(store);

  const socialSync = await pushToSocial(video);

  return res.status(201).json({
    status: 'registered',
    video,
    social_sync: socialSync
  });
});

app.get('/v1/videos', (req, res) => {
  const creatorId = String(req.query.creator_id || '').trim();
  const limit = parseLimit(req.query.limit, 20, 100);
  const cursor = decodeCursor(req.query.cursor);

  const store = loadStore();
  sortVideosDesc(store.videos);

  let items = store.videos;
  if (creatorId) {
    items = items.filter((v) => v.creator_id === creatorId);
  }

  items = filterAfterCursor(items, cursor);

  const page = items.slice(0, limit);
  const hasMore = items.length > limit;
  const nextCursor = hasMore && page.length > 0 ? encodeCursor(page[page.length - 1]) : '';

  res.json({
    data: page,
    paging: {
      strategy: 'keyset_cursor',
      limit,
      has_more: hasMore,
      next_cursor: nextCursor
    }
  });
});

app.get('/v1/creators/:creatorId/videos', (req, res) => {
  const creatorId = String(req.params.creatorId || '').trim();
  const limit = parseLimit(req.query.limit, 20, 100);
  const cursor = decodeCursor(req.query.cursor);
  const store = loadStore();

  sortVideosDesc(store.videos);
  const videos = filterAfterCursor(store.videos.filter((v) => v.creator_id === creatorId), cursor);
  const page = videos.slice(0, limit);
  const hasMore = videos.length > limit;
  const nextCursor = hasMore && page.length > 0 ? encodeCursor(page[page.length - 1]) : '';

  res.json({
    creator_id: creatorId,
    data: page,
    paging: {
      strategy: 'keyset_cursor',
      limit,
      has_more: hasMore,
      next_cursor: nextCursor
    }
  });
});

app.get('/v1/videos/:videoId/monetization', (req, res) => {
  const videoId = String(req.params.videoId || '').trim();
  const store = loadStore();
  const video = store.videos.find((v) => v.id === videoId);
  if (!video) {
    return res.status(404).json({ error: 'video not found' });
  }
  return res.json({
    video_id: videoId,
    monetization: store.monetization[videoId] || video.monetization
  });
});

app.put('/v1/videos/:videoId/monetization', (req, res) => {
  const videoId = String(req.params.videoId || '').trim();
  const store = loadStore();
  const index = store.videos.findIndex((v) => v.id === videoId);
  if (index < 0) {
    return res.status(404).json({ error: 'video not found' });
  }

  const enabled = Boolean(req.body.enabled ?? true);
  const monetization = {
    enabled,
    model: enabled ? String(req.body.model || 'ad_cpm').trim() || 'ad_cpm' : 'off',
    cpm_usd: enabled ? Math.max(0, toNumber(req.body.cpm_usd, 1.25)) : 0,
    rev_share_pct: enabled ? Math.min(100, Math.max(0, toNumber(req.body.rev_share_pct, 62.5))) : 0,
    updated_at: new Date().toISOString()
  };

  store.videos[index].monetization = monetization;
  store.monetization[videoId] = monetization;
  saveStore(store);

  return res.json({
    status: 'updated',
    video_id: videoId,
    monetization
  });
});

app.listen(PORT, () => {
  ensureStoreFile();
  console.log(`nexora-media listening on :${PORT}`);
});
