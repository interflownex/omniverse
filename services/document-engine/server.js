const express = require('express');
const fs = require('fs');
const path = require('path');
const amqp = require('amqplib');
const PDFDocument = require('pdfkit');
const {
  S3Client,
  PutObjectCommand,
  HeadBucketCommand,
  CreateBucketCommand
} = require('@aws-sdk/client-s3');

const app = express();
app.use(express.json({ limit: '1mb' }));

const PORT = Number(process.env.PORT || '8094');
const RABBITMQ_URL = process.env.RABBITMQ_URL || 'amqp://nexora:nexora123@rabbitmq:5672/';
const RABBITMQ_QUEUE = process.env.RABBITMQ_QUEUE || 'nexora.purchase.receipts';
const DOC_ENGINE_TOKEN = process.env.DOCUMENT_ENGINE_TOKEN || 'doc-engine-token';

const MINIO_ENDPOINT = process.env.MINIO_ENDPOINT || 'http://minio:9000';
const MINIO_REGION = process.env.MINIO_REGION || 'us-east-1';
const MINIO_ACCESS_KEY = process.env.MINIO_ACCESS_KEY || 'nexora';
const MINIO_SECRET_KEY = process.env.MINIO_SECRET_KEY || 'nexora123';
const MINIO_BUCKET = process.env.MINIO_DOCS_BUCKET || 'nexora-docs';
const MINIO_PUBLIC_BASE_URL = process.env.MINIO_PUBLIC_BASE_URL || MINIO_ENDPOINT;

const STORE_FILE = process.env.DOCUMENT_STORE_FILE || path.join('/data', 'document-store.json');

const s3 = new S3Client({
  region: MINIO_REGION,
  endpoint: MINIO_ENDPOINT,
  forcePathStyle: true,
  credentials: {
    accessKeyId: MINIO_ACCESS_KEY,
    secretAccessKey: MINIO_SECRET_KEY
  }
});

let rabbitConn = null;
let rabbitChannel = null;
let consumerStarted = false;
let processedCount = 0;
let lastError = '';

function ensureStoreFile() {
  const dir = path.dirname(STORE_FILE);
  fs.mkdirSync(dir, { recursive: true });
  if (!fs.existsSync(STORE_FILE)) {
    fs.writeFileSync(STORE_FILE, JSON.stringify({ documents: [], by_purchase: {} }, null, 2), 'utf8');
  }
}

function loadStore() {
  ensureStoreFile();
  const raw = fs.readFileSync(STORE_FILE, 'utf8');
  const parsed = JSON.parse(raw || '{}');
  return {
    documents: Array.isArray(parsed.documents) ? parsed.documents : [],
    by_purchase: parsed.by_purchase && typeof parsed.by_purchase === 'object' ? parsed.by_purchase : {}
  };
}

function saveStore(store) {
  ensureStoreFile();
  const tmp = `${STORE_FILE}.tmp`;
  fs.writeFileSync(tmp, JSON.stringify(store, null, 2), 'utf8');
  fs.renameSync(tmp, STORE_FILE);
}

function normalizeText(v) {
  return String(v || '').trim().toLowerCase();
}

function normalizeID(v) {
  const raw = String(v || '').trim().toLowerCase();
  if (!raw) return '';
  return raw
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+/, '')
    .replace(/-+$/, '');
}

function centsToMoney(cents) {
  const sign = cents < 0 ? '-' : '';
  const abs = Math.abs(Number(cents || 0));
  return `${sign}${Math.floor(abs / 100)}.${String(abs % 100).padStart(2, '0')}`;
}

function isoDatePath() {
  const now = new Date();
  const y = String(now.getUTCFullYear());
  const m = String(now.getUTCMonth() + 1).padStart(2, '0');
  const d = String(now.getUTCDate()).padStart(2, '0');
  return `${y}/${m}/${d}`;
}

function sanitizeKey(v) {
  return String(v || 'na').toLowerCase().replace(/[^a-z0-9_-]+/g, '-');
}

async function ensureBucket() {
  try {
    await s3.send(new HeadBucketCommand({ Bucket: MINIO_BUCKET }));
    return;
  } catch (err) {
    if (err?.$metadata?.httpStatusCode && err.$metadata.httpStatusCode !== 404) {
      throw err;
    }
  }
  try {
    await s3.send(new CreateBucketCommand({ Bucket: MINIO_BUCKET }));
  } catch (err) {
    const msg = String(err?.message || '');
    if (!msg.includes('BucketAlreadyOwnedByYou') && !msg.includes('BucketAlreadyExists')) {
      throw err;
    }
  }
}

function createReceiptPDFBuffer(event, documentId) {
  return new Promise((resolve, reject) => {
    const doc = new PDFDocument({ size: 'A4', margin: 50 });
    const chunks = [];
    doc.on('data', (chunk) => chunks.push(chunk));
    doc.on('error', reject);
    doc.on('end', () => resolve(Buffer.concat(chunks)));

    const now = new Date().toISOString();
    doc.fontSize(26).fillColor('#0f172a').text('NEXORA', { align: 'left' });
    doc.moveDown(0.2);
    doc.fontSize(12).fillColor('#334155').text('Comprovante Digital de Compra', { align: 'left' });
    doc.moveDown(1.2);

    doc.fontSize(11).fillColor('#111827');
    doc.text(`Documento: ${documentId}`);
    doc.text(`Emitido em: ${now}`);
    doc.text(`Origem: ${event.source}`);
    doc.text(`Pedido: ${event.order_id}`);
    doc.moveDown(0.8);

    doc.text(`Comprador: ${event.buyer_user_id || 'n/a'}`);
    doc.text(`Vendedor: ${event.seller_user_id || 'n/a'}`);
    doc.text(`Moeda: ${event.currency || 'BRL'}`);
    doc.moveDown(0.8);

    doc.fontSize(13).fillColor('#0f172a').text('Resumo Financeiro');
    doc.moveDown(0.3);
    doc.fontSize(11).fillColor('#111827');
    doc.text(`Bruto: R$ ${centsToMoney(event.gross_cents || 0)}`);
    doc.text(`Taxas: R$ ${centsToMoney(event.fee_cents || 0)}`);
    doc.text(`Liquido: R$ ${centsToMoney(event.net_cents || 0)}`);
    doc.moveDown(0.8);

    if (event.description) {
      doc.fontSize(13).fillColor('#0f172a').text('Descricao');
      doc.moveDown(0.2);
      doc.fontSize(11).fillColor('#111827').text(String(event.description));
      doc.moveDown(0.6);
    }

    doc.fontSize(9).fillColor('#475569').text('Nexora Platform - Documento automatizado via Document Engine.', {
      align: 'left'
    });

    doc.end();
  });
}

function createCertificatePDFBuffer(event, documentId) {
  const metadata = event.metadata && typeof event.metadata === 'object' ? event.metadata : {};
  const courseTitle = String(metadata.course_title || event.description || 'Curso Nexora').trim();
  const issuer = String(metadata.issuer || 'Nexora School').trim();
  const score = Number(metadata.score || 0);

  return new Promise((resolve, reject) => {
    const doc = new PDFDocument({ size: 'A4', margin: 48 });
    const chunks = [];
    doc.on('data', (chunk) => chunks.push(chunk));
    doc.on('error', reject);
    doc.on('end', () => resolve(Buffer.concat(chunks)));

    const issuedAt = new Date().toISOString();
    doc.rect(26, 26, 543, 789).lineWidth(3).strokeColor('#0f172a').stroke();
    doc.rect(36, 36, 523, 769).lineWidth(1).strokeColor('#64748b').stroke();

    doc.moveDown(1.8);
    doc.fontSize(30).fillColor('#0f172a').text('NEXORA', { align: 'center' });
    doc.moveDown(0.3);
    doc.fontSize(18).fillColor('#334155').text('CERTIFICADO OFICIAL', { align: 'center' });
    doc.moveDown(1.2);

    doc.fontSize(12).fillColor('#334155').text('Este documento certifica que', { align: 'center' });
    doc.moveDown(0.3);
    doc.fontSize(24).fillColor('#111827').text(String(event.buyer_user_id || 'aluno').toUpperCase(), { align: 'center' });
    doc.moveDown(0.6);
    doc.fontSize(12).fillColor('#334155').text('concluiu com aproveitamento o curso', { align: 'center' });
    doc.moveDown(0.35);
    doc.fontSize(19).fillColor('#0f172a').text(courseTitle, { align: 'center' });
    doc.moveDown(0.8);

    doc.fontSize(12).fillColor('#334155').text(`Emissor: ${issuer}`, { align: 'center' });
    doc.text(`ID do certificado: ${event.order_id}`, { align: 'center' });
    doc.text(`ID do documento: ${documentId}`, { align: 'center' });
    doc.text(`Data de emissao: ${issuedAt}`, { align: 'center' });
    if (score > 0) {
      doc.text(`Nota final: ${score.toFixed(1)} / 100`, { align: 'center' });
    }

    doc.moveDown(3.0);
    doc.fontSize(11).fillColor('#0f172a').text('_______________________________', { align: 'center' });
    doc.fontSize(10).fillColor('#334155').text('Assinatura digital - Nexora Document Engine', { align: 'center' });

    doc.moveDown(1.0);
    doc.fontSize(9).fillColor('#475569').text(
      'Valide este certificado no painel School & Job. Documento protegido por hash e trilha de auditoria Nexora.',
      { align: 'center' }
    );

    doc.end();
  });
}

function createPDFBuffer(event, documentId) {
  const docType = normalizeText(event.document_type || 'receipt');
  if (docType === 'certificate') {
    return createCertificatePDFBuffer(event, documentId);
  }
  return createReceiptPDFBuffer(event, documentId);
}

async function persistDocument(event) {
  const source = normalizeText(event.source);
  const orderId = String(event.order_id || '').trim();
  const documentType = normalizeText(event.document_type || 'receipt') || 'receipt';
  if (!source || !orderId) {
    throw new Error('source and order_id are required in event');
  }

  const metadata = event.metadata && typeof event.metadata === 'object' ? event.metadata : {};
  const store = loadStore();
  const purchaseKey = `${documentType}:${source}:${orderId}`;
  if (store.by_purchase[purchaseKey]) {
    return { alreadyExists: true, document: store.by_purchase[purchaseKey] };
  }

  const documentId = `doc-${Date.now()}-${Math.floor(Math.random() * 100000)}`;
  const pdfBuffer = await createPDFBuffer(event, documentId);
  const folder = documentType === 'certificate' ? 'certificates' : 'receipts';
  const objectKey = `${folder}/${isoDatePath()}/${sanitizeKey(source)}-${sanitizeKey(orderId)}.pdf`;

  await s3.send(
    new PutObjectCommand({
      Bucket: MINIO_BUCKET,
      Key: objectKey,
      Body: pdfBuffer,
      ContentType: 'application/pdf',
      Metadata: {
        source,
        order_id: orderId,
        document_id: documentId,
        document_type: documentType
      }
    })
  );

  const docUrl = `${MINIO_PUBLIC_BASE_URL.replace(/\/$/, '')}/${MINIO_BUCKET}/${objectKey}`;
  const document = {
    document_id: documentId,
    source,
    order_id: orderId,
    document_type: documentType,
    object_key: objectKey,
    document_url: docUrl,
    buyer_user_id: event.buyer_user_id || '',
    seller_user_id: event.seller_user_id || '',
    currency: event.currency || 'BRL',
    gross_cents: Number(event.gross_cents || 0),
    fee_cents: Number(event.fee_cents || 0),
    net_cents: Number(event.net_cents || 0),
    description: String(event.description || ''),
    metadata,
    created_at: new Date().toISOString()
  };

  store.documents.unshift(document);
  store.by_purchase[purchaseKey] = document;
  if (store.documents.length > 5000) {
    store.documents = store.documents.slice(0, 5000);
  }
  saveStore(store);

  return { alreadyExists: false, document };
}

async function connectRabbit() {
  rabbitConn = await amqp.connect(RABBITMQ_URL);
  rabbitConn.on('error', (err) => {
    lastError = `rabbit_error: ${err.message}`;
  });
  rabbitConn.on('close', () => {
    rabbitConn = null;
    rabbitChannel = null;
    consumerStarted = false;
    setTimeout(() => {
      bootstrapRabbit().catch((err) => {
        lastError = `rabbit_reconnect_failed: ${err.message}`;
      });
    }, 1500);
  });

  rabbitChannel = await rabbitConn.createChannel();
  await rabbitChannel.assertQueue(RABBITMQ_QUEUE, { durable: true });
  await rabbitChannel.prefetch(8);
}

async function bootstrapRabbit() {
  if (rabbitChannel) return;
  await connectRabbit();
  if (!consumerStarted) {
    await rabbitChannel.consume(
      RABBITMQ_QUEUE,
      async (msg) => {
        if (!msg) return;
        try {
          const payload = JSON.parse(msg.content.toString('utf8'));
          await persistDocument(payload);
          processedCount += 1;
          rabbitChannel.ack(msg);
        } catch (err) {
          lastError = `consume_error: ${err.message}`;
          rabbitChannel.nack(msg, false, false);
        }
      },
      { noAck: false }
    );
    consumerStarted = true;
  }
}

async function enqueueDocumentEvent(event) {
  if (!rabbitChannel) {
    await bootstrapRabbit();
  }
  const body = Buffer.from(JSON.stringify(event), 'utf8');
  const ok = rabbitChannel.sendToQueue(RABBITMQ_QUEUE, body, {
    persistent: true,
    contentType: 'application/json',
    timestamp: Date.now()
  });
  if (!ok) {
    await new Promise((resolve) => setTimeout(resolve, 20));
  }
}

app.get('/healthz', (_req, res) => {
  res.json({
    status: 'ok',
    service: 'document-engine',
    rabbit_connected: Boolean(rabbitChannel),
    queue: RABBITMQ_QUEUE,
    minio_bucket: MINIO_BUCKET,
    processed_count: processedCount,
    last_error: lastError
  });
});

app.post('/v1/events/purchase', async (req, res) => {
  const token = String(req.headers['x-doc-engine-token'] || '').trim();
  if (DOC_ENGINE_TOKEN && token !== DOC_ENGINE_TOKEN) {
    return res.status(401).json({ error: 'invalid token' });
  }

  const source = normalizeText(req.body.source);
  const orderId = String(req.body.order_id || '').trim();
  const grossCents = Number(req.body.gross_cents || 0);
  const feeCents = Number(req.body.fee_cents || 0);
  const netCents = Number(req.body.net_cents || 0);

  if (!source || !orderId) {
    return res.status(400).json({ error: 'source and order_id are required' });
  }

  const event = {
    source,
    order_id: orderId,
    document_type: 'receipt',
    buyer_user_id: normalizeID(req.body.buyer_user_id),
    seller_user_id: normalizeID(req.body.seller_user_id),
    currency: String(req.body.currency || 'BRL').toUpperCase(),
    gross_cents: Number.isFinite(grossCents) ? Math.max(0, Math.floor(grossCents)) : 0,
    fee_cents: Number.isFinite(feeCents) ? Math.max(0, Math.floor(feeCents)) : 0,
    net_cents: Number.isFinite(netCents) ? Math.max(0, Math.floor(netCents)) : 0,
    description: String(req.body.description || '').trim(),
    metadata: req.body.metadata && typeof req.body.metadata === 'object' ? req.body.metadata : {},
    event_at: new Date().toISOString()
  };

  try {
    await enqueueDocumentEvent(event);
    return res.status(202).json({ status: 'queued', queue: RABBITMQ_QUEUE, source, order_id: orderId });
  } catch (err) {
    lastError = `enqueue_error: ${err.message}`;
    return res.status(503).json({ error: `queue unavailable: ${err.message}` });
  }
});

app.post('/v1/events/certificate', async (req, res) => {
  const token = String(req.headers['x-doc-engine-token'] || '').trim();
  if (DOC_ENGINE_TOKEN && token !== DOC_ENGINE_TOKEN) {
    return res.status(401).json({ error: 'invalid token' });
  }

  const userId = normalizeID(req.body.user_id);
  const courseId = normalizeID(req.body.course_id);
  const courseTitle = String(req.body.course_title || '').trim();
  const issuer = String(req.body.issuer || 'Nexora School').trim();
  const certificateId = String(req.body.certificate_id || `cert-${Date.now()}`).trim();
  const score = Number(req.body.score || 0);

  if (!userId || !courseTitle) {
    return res.status(400).json({ error: 'user_id and course_title are required' });
  }

  const event = {
    source: 'school-job',
    order_id: certificateId,
    document_type: 'certificate',
    buyer_user_id: userId,
    seller_user_id: normalizeID(issuer),
    currency: 'BRL',
    gross_cents: 0,
    fee_cents: 0,
    net_cents: 0,
    description: String(req.body.description || `Certificado de conclusao - ${courseTitle}`).trim(),
    metadata: {
      course_id: courseId,
      course_title: courseTitle,
      issuer,
      score: Number.isFinite(score) ? Math.max(0, Math.min(100, score)) : 0
    },
    event_at: new Date().toISOString()
  };

  try {
    await enqueueDocumentEvent(event);
    return res.status(202).json({
      status: 'queued',
      queue: RABBITMQ_QUEUE,
      certificate_id: certificateId,
      source: event.source,
      order_id: event.order_id
    });
  } catch (err) {
    lastError = `enqueue_error: ${err.message}`;
    return res.status(503).json({ error: `queue unavailable: ${err.message}` });
  }
});

app.get('/v1/documents', (req, res) => {
  const source = normalizeText(req.query.source);
  const limitRaw = Number(req.query.limit || 100);
  const limit = Number.isFinite(limitRaw) ? Math.max(1, Math.min(500, Math.floor(limitRaw))) : 100;
  const docType = normalizeText(req.query.document_type);

  const store = loadStore();
  let items = store.documents;
  if (source) {
    items = items.filter((doc) => doc.source === source);
  }
  if (docType) {
    items = items.filter((doc) => String(doc.document_type || 'receipt') === docType);
  }

  const page = items.slice(0, limit);
  res.json({ data: page, count: page.length });
});

app.get('/v1/documents/:documentId', (req, res) => {
  const documentId = String(req.params.documentId || '').trim();
  const store = loadStore();
  const doc = store.documents.find((item) => item.document_id === documentId);
  if (!doc) {
    return res.status(404).json({ error: 'document not found' });
  }
  return res.json(doc);
});

async function bootstrap() {
  ensureStoreFile();
  await ensureBucket();
  await bootstrapRabbit();

  app.listen(PORT, () => {
    console.log(`document-engine listening on :${PORT}`);
  });
}

bootstrap().catch((err) => {
  console.error('document-engine bootstrap failed', err);
  process.exit(1);
});
