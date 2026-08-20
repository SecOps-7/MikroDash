'use strict';
const https      = require('https');
const nodemailer = require('nodemailer');

// Pull a human reason out of an error response body. Telegram and ntfy both
// return one; without it a failure reads as a bare status code. Kept short so a
// long HTML error page cannot flood the log or the test-notification response.
function _reason(raw) {
  if (!raw) return '';
  let msg = '';
  try {
    const j = JSON.parse(raw);
    msg = j.description || j.error || j.message || '';
  } catch (_) {
    msg = String(raw).trim();
  }
  if (!msg) return '';
  msg = msg.replace(/\s+/g, ' ').slice(0, 160);
  return ' — ' + msg;
}

// A channel counts as usable only when it is enabled *and* has the config its
// send path requires. The alerter previously decided "is any channel active?"
// from the *Enabled flags alone, while send() checked flags plus credentials —
// so a channel ticked without a token consumed the alert cooldown, sent
// nothing, and logged nothing. Both sides now ask this one question.
function hasConfiguredChannel(s) {
  if (!s) return false;
  return !!(
    (s.telegramEnabled   && s.telegramBotToken && s.telegramChatId) ||
    (s.pushbulletEnabled && s.pushbulletApiKey) ||
    (s.smtpEnabled       && s.smtpHost && s.smtpFrom && s.smtpTo) ||
    (s.ntfyEnabled       && s.ntfyUrl)
  );
}

function _httpsPost(hostname, path, headers, body) {
  return new Promise((resolve, reject) => {
    const data = JSON.stringify(body);
    const req = https.request({
      hostname,
      path,
      method: 'POST',
      headers: {
        'Content-Type':   'application/json',
        'Content-Length': Buffer.byteLength(data),
        ...headers,
      },
    }, (res) => {
      let raw = '';
      res.on('data', c => { raw += c; });
      res.on('end', () => {
        if (res.statusCode >= 200 && res.statusCode < 300) return resolve(raw);
        // Include the reason, not just the status. Telegram puts the actual
        // cause ("chat not found", "Unauthorized") in the JSON `description`
        // and it was already buffered here, so discarding it left users with a
        // bare "HTTP 400" and nothing to act on.
        reject(new Error(`HTTP ${res.statusCode}${_reason(raw)}`));
      });
    });
    req.on('error', reject);
    req.setTimeout(10000, () => { req.destroy(new Error('Request timed out')); });
    req.write(data);
    req.end();
  });
}

async function sendTelegram(token, chatId, title, body) {
  await _httpsPost(
    'api.telegram.org',
    `/bot${encodeURIComponent(token)}/sendMessage`,
    {},
    { chat_id: chatId, text: title + '\n' + body }
  );
}

async function sendPushbullet(apiKey, title, body) {
  await _httpsPost(
    'api.pushbullet.com',
    '/v2/pushes',
    { 'Access-Token': apiKey },
    { type: 'note', title, body }
  );
}

function _transport(settings) {
  return nodemailer.createTransport({
    host:   settings.smtpHost,
    port:   settings.smtpPort || 587,
    secure: !!settings.smtpSecure,
    auth:   (settings.smtpUser || settings.smtpPass)
              ? { user: settings.smtpUser, pass: settings.smtpPass }
              : undefined,
    // The other three channels enforce 10 s. Without these, nodemailer's
    // defaults (~2 min) apply, and because send() awaits each channel in turn a
    // black-holed SMTP host stalls every later channel behind it — and holds
    // the test-notification HTTP request open for the same period.
    connectionTimeout: 10000,
    greetingTimeout:   10000,
    socketTimeout:     15000,
  });
}

/**
 * Send one email, with recipients and attachments of its own.
 *
 * Deliberately separate from send(). That function fans the same title and body
 * out to four channels, three of which have no concept of an attachment, so
 * widening it would be a contract that lies: Telegram, Pushbullet and ntfy
 * would silently drop whatever was attached. A scheduled report also has its
 * own fixed recipient list, and must not go to whichever channels the install
 * happens to have switched on.
 *
 * `to` and `bcc` are passed through as given, arrays included. Never build an
 * address list by joining strings: a newline inside one address would inject
 * mail headers.
 */
async function sendMail(settings, { to, bcc, subject, text, attachments }) {
  const transport = _transport(settings);
  try {
    await transport.sendMail({
      from: settings.smtpFrom,
      to, bcc, subject, text,
      attachments: (attachments && attachments.length) ? attachments : undefined,
    });
  } finally {
    transport.close();
  }
}

async function sendSmtp(settings, title, body) {
  return sendMail(settings, { to: settings.smtpTo, subject: title, text: body });
}

async function send(settings, title, body) {
  const errs = [];
  if (settings.telegramEnabled && settings.telegramBotToken && settings.telegramChatId) {
    try {
      await sendTelegram(settings.telegramBotToken, settings.telegramChatId, title, body);
    } catch (e) {
      errs.push('Telegram: ' + e.message);
      // Not necessarily an HTTP failure — this also catches DNS and timeout
      // errors, which the old "error: HTTP <message>" prefix mislabelled.
      console.error('[notifier] Telegram error: %s', e.message);
    }
  }
  if (settings.pushbulletEnabled && settings.pushbulletApiKey) {
    try {
      await sendPushbullet(settings.pushbulletApiKey, title, body);
    } catch (e) {
      errs.push('Pushbullet: ' + e.message);
      console.error('[notifier] Pushbullet error: %s', e.message);
    }
  }
  if (settings.smtpEnabled && settings.smtpHost && settings.smtpFrom && settings.smtpTo) {
    try {
      await sendSmtp(settings, title, body);
    } catch (e) {
      errs.push('SMTP: ' + e.message);
      console.error('[notifier] SMTP error:', e.code || e.message);
    }
  }
  if (settings.ntfyEnabled && settings.ntfyUrl) {
    try {
      await sendNtfy(settings.ntfyUrl, settings.ntfyToken || '', title, body);
    } catch (e) {
      errs.push('ntfy: ' + e.message);
      console.error('[notifier] ntfy error:', e.message);
    }
  }
  if (errs.length) throw new Error(errs.join('; '));
}

async function sendNtfy(topicUrl, token, title, body) {
  const parsed = new URL(topicUrl);
  const isHttps = parsed.protocol === 'https:';
  const lib = require(isHttps ? 'https' : 'http');
  const raw = Buffer.from(body, 'utf8');
  const headers = {
    'Title':          title,
    'Content-Type':   'text/plain; charset=utf-8',
    'Content-Length': raw.length,
  };
  if (token) headers['Authorization'] = 'Bearer ' + token;
  return new Promise((resolve, reject) => {
    const req = lib.request({
      hostname: parsed.hostname,
      port:     parsed.port || (isHttps ? 443 : 80),
      path:     parsed.pathname,
      method:   'POST',
      headers,
    }, (res) => {
      let buf = '';
      res.on('data', c => { buf += c; });
      res.on('end', () => {
        if (res.statusCode >= 200 && res.statusCode < 300) resolve(buf);
        else reject(new Error('HTTP ' + res.statusCode));
      });
    });
    req.on('error', reject);
    req.setTimeout(10000, () => { req.destroy(new Error('Request timed out')); });
    req.write(raw);
    req.end();
  });
}

async function testChannel(settings, channel) {
  const title = 'MikroDash Test';
  const body  = 'Test notification from MikroDash — your alert channel is working correctly.';
  if (channel === 'telegram') {
    if (!settings.telegramBotToken) throw new Error('Telegram Bot Token is not configured');
    if (!settings.telegramChatId)   throw new Error('Telegram Chat ID is not configured');
    await sendTelegram(settings.telegramBotToken, settings.telegramChatId, title, body);
  } else if (channel === 'pushbullet') {
    if (!settings.pushbulletApiKey) throw new Error('Pushbullet API Key is not configured');
    await sendPushbullet(settings.pushbulletApiKey, title, body);
  } else if (channel === 'smtp') {
    if (!settings.smtpHost) throw new Error('SMTP Host is not configured');
    if (!settings.smtpFrom) throw new Error('SMTP From address is not configured');
    if (!settings.smtpTo)   throw new Error('SMTP To address is not configured');
    await sendSmtp(settings, title, body);
  } else if (channel === 'ntfy') {
    if (!settings.ntfyUrl) throw new Error('ntfy topic URL is not configured');
    await sendNtfy(settings.ntfyUrl, settings.ntfyToken || '', title, body);
  } else {
    throw new Error('Unknown notification channel');
  }
}

module.exports = { send, sendMail, testChannel, hasConfiguredChannel };
