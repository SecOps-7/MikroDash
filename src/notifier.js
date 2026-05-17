'use strict';
const https      = require('https');
const nodemailer = require('nodemailer');

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
        if (res.statusCode >= 200 && res.statusCode < 300) resolve(raw);
        else reject(new Error(`HTTP ${res.statusCode}`));
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

async function sendSmtp(settings, title, body) {
  const transport = nodemailer.createTransport({
    host:   settings.smtpHost,
    port:   settings.smtpPort || 587,
    secure: !!settings.smtpSecure,
    auth:   (settings.smtpUser || settings.smtpPass)
              ? { user: settings.smtpUser, pass: settings.smtpPass }
              : undefined,
  });
  try {
    await transport.sendMail({
      from:    settings.smtpFrom,
      to:      settings.smtpTo,
      subject: title,
      text:    body,
    });
  } finally {
    transport.close();
  }
}

async function send(settings, title, body) {
  const errs = [];
  if (settings.telegramEnabled && settings.telegramBotToken && settings.telegramChatId) {
    try {
      await sendTelegram(settings.telegramBotToken, settings.telegramChatId, title, body);
    } catch (e) {
      errs.push('Telegram: ' + e.message);
      console.error('[notifier] Telegram error: HTTP', e.message);
    }
  }
  if (settings.pushbulletEnabled && settings.pushbulletApiKey) {
    try {
      await sendPushbullet(settings.pushbulletApiKey, title, body);
    } catch (e) {
      errs.push('Pushbullet: ' + e.message);
      console.error('[notifier] Pushbullet error: HTTP', e.message);
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
  if (errs.length) throw new Error(errs.join('; '));
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
  } else {
    throw new Error('Unknown notification channel');
  }
}

module.exports = { send, testChannel };
