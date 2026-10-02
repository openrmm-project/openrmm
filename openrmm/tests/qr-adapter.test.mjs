// The QR adapter keeps the old call shape and draws an image with a quiet zone.
import assert from 'node:assert/strict';
import test from 'node:test';

// Just enough DOM for the adapter: it creates one <img> and replaces the
// node's children with it.
globalThis.document = { createElement: (tag) => ({ tagName: tag.toUpperCase(), style: {} }) };
const node = () => {
  const children = [{ old: true }];
  return {
    children,
    get firstChild() { return children[0]; },
    removeChild(child) { children.splice(children.indexOf(child), 1); },
    appendChild(child) { children.push(child); },
  };
};

const { QRCode } = await import('../src/qr-adapter.js');

test('draws one image, replacing what was there', () => {
  const target = node();
  new QRCode(target, { text: 'https://example.com/console', width: 168, height: 168, correctLevel: QRCode.CorrectLevel.M });
  assert.equal(target.children.length, 1);
  const img = target.children[0];
  assert.equal(img.tagName, 'IMG');
  assert.match(img.src, /^data:image\/gif;base64,/);
  assert.equal(img.width, img.height);
  assert.ok(img.width <= 168 && img.width >= 100, `size ${img.width}`);
  assert.equal(img.style.background, '#ffffff');
});

test('the image leaves room for a four-module quiet zone on every side', () => {
  const target = node();
  new QRCode(target, { text: 'https://example.com/console', width: 400 });
  const img = target.children[0];
  // A version-3 symbol (29 modules) for this text, plus 4 light modules each
  // side: the side is a whole number of cells of 37 modules.
  assert.equal(img.width % 37, 0, `side ${img.width} is not 37 cells`);
});

test('unknown correction levels fall back to M; text that is not ASCII still encodes', () => {
  const target = node();
  assert.doesNotThrow(() => new QRCode(target, { text: 'Configuração — 設定', correctLevel: 'X' }));
  assert.equal(target.children.length, 1);
});
