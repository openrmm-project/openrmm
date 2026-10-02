// QR codes for the console, drawn by qrcode-generator (MIT, pinned in
// package.json; the npm tarball's integrity is pinned in package-lock.json and
// dist/qrcode.mjs was checked byte-identical to the js2.0.4 tag on GitHub).
//
// It keeps the call shape the console already used with qrcodejs —
// new QRCode(node, { text, width, correctLevel }) — and draws the code as an
// <img> with the four-module light quiet zone the QR specification requires,
// on a white background whatever the theme. qrcodejs drew none, and
// authenticator apps failed to read its codes.
import qrcode from 'qrcode-generator';

const QUIET_ZONE = 4;

// Text is encoded as UTF-8. The library's default keeps only the low byte of
// each character, which is right for ASCII and wrong for anything else.
qrcode.stringToBytes = (text) => Array.from(new TextEncoder().encode(text));

export function QRCode(node, options = {}) {
  const level = /^[LMQH]$/.test(options.correctLevel) ? options.correctLevel : 'M';
  const qr = qrcode(0, level);
  qr.addData(String(options.text ?? ''), 'Byte');
  qr.make();
  const modules = qr.getModuleCount() + QUIET_ZONE * 2;
  const cell = Math.max(2, Math.floor((options.width || 176) / modules));
  const img = document.createElement('img');
  img.src = qr.createDataURL(cell, cell * QUIET_ZONE);
  img.width = cell * modules;
  img.height = cell * modules;
  img.alt = '';
  img.style.background = '#ffffff';
  img.style.imageRendering = 'pixelated';
  while (node.firstChild) node.removeChild(node.firstChild);
  node.appendChild(img);
}

QRCode.CorrectLevel = { L: 'L', M: 'M', Q: 'Q', H: 'H' };
